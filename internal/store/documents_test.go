package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newDoc(content string) store.NewDocument {
	return store.NewDocument{
		Title:            content,
		Source:           store.SourceFile,
		OriginalFileName: content + ".pdf",
		OriginalSHA256:   hashOf(content),
		OriginalSize:     int64(len(content)),
		OriginalMIME:     "application/pdf",
		IsInbox:          true,
	}
}

func mustDoc(t *testing.T, st *store.Store, sc store.Scope, in store.NewDocument) *store.Document {
	t.Helper()
	d, err := st.CreateDocument(context.Background(), sc, in)
	if err != nil {
		t.Fatalf("create document %q: %v", in.Title, err)
	}
	return d
}

func ptr[T any](v T) *T { return &v }

func TestDocumentRoundTrip(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	corr := mustCorrespondent(t, st, alice, "Stadtwerke")
	typ, err := st.CreateDocumentType(ctx, alice, store.DocumentTypeInput{Name: "Invoice"})
	if err != nil {
		t.Fatal(err)
	}
	tag := mustTag(t, st, alice, "Tax", nil)

	date := time.Date(2026, 10, 15, 9, 30, 0, 123456000, time.UTC)
	in := newDoc("invoice")
	in.DocumentDate = date
	in.Notes = "paid"
	in.ArchiveSerialNumber = ptr(int32(7))
	in.PageCount = 2
	in.ExtractedText = "Rechnung"
	in.TextSource = "PDF-Text"
	in.ArchiveSHA256 = ptr(hashOf("archive"))
	in.ArchiveSize = ptr(int64(99))
	in.CorrespondentID = &corr.ID
	in.DocumentTypeID = &typ.ID
	in.TagIDs = []string{tag.ID, tag.ID}
	d := mustDoc(t, st, alice, in)

	got, err := st.GetDocument(ctx, alice, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerID != alice.UserID || got.Title != "invoice" || !got.DocumentDate.Equal(date) ||
		got.Notes != "paid" || *got.ArchiveSerialNumber != 7 || got.PageCount != 2 ||
		got.ExtractedText != "Rechnung" || got.TextSource != "PDF-Text" || got.Source != store.SourceFile ||
		got.OriginalFileName != "invoice.pdf" || got.OriginalSHA256 != hashOf("invoice") ||
		got.OriginalSize != 7 || got.OriginalMIME != "application/pdf" || !got.IsInbox ||
		*got.ArchiveSHA256 != hashOf("archive") || *got.ArchiveSize != 99 ||
		*got.CorrespondentID != corr.ID || *got.DocumentTypeID != typ.ID ||
		len(got.TagIDs) != 1 || got.TagIDs[0] != tag.ID {
		t.Errorf("document = %+v", got)
	}

	// A patch replaces the given fields and keeps the others.
	up, err := st.PatchDocument(ctx, alice, d.ID, store.DocumentPatch{Title: ptr("Strom"), IsFavorite: ptr(true),
		IsInbox: ptr(false), SetASN: true, SetCorrespondent: true, SetDocumentType: true})
	if err != nil {
		t.Fatal(err)
	}
	if up.Title != "Strom" || !up.IsFavorite || up.IsInbox || up.ArchiveSerialNumber != nil ||
		up.CorrespondentID != nil || up.DocumentTypeID != nil || !up.DocumentDate.Equal(date) ||
		len(up.TagIDs) != 1 || up.ModifiedAt.Before(got.ModifiedAt) {
		t.Errorf("updated = %+v", up)
	}

	if _, err := st.PatchDocument(ctx, alice, d.ID, store.DocumentPatch{SetTags: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetDocument(ctx, alice, d.ID); len(got.TagIDs) != 0 {
		t.Errorf("tags after clearing = %v", got.TagIDs)
	}

	if err := st.DeleteDocument(ctx, alice, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetDocument(ctx, alice, d.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if err := st.DeleteDocument(ctx, alice, d.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if _, err := st.GetDocument(ctx, alice, "not-a-uuid"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
}

func TestDocumentArchiveFileNeedsSize(t *testing.T) {
	st := storetest.New(t)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	in := newDoc("a")
	in.ArchiveSHA256 = ptr(hashOf("archive"))
	if _, err := st.CreateDocument(context.Background(), alice, in); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("archive hash without size: %v, want ErrInvalid", err)
	}
}

// CreateDocument validates its input before the database sees it: each
// refused field gives a *ValidationError naming it, and no row is written.
func TestCreateDocumentValidation(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	for _, tc := range []struct {
		field  string
		change func(*store.NewDocument)
	}{
		{"source", func(d *store.NewDocument) { d.Source = "" }},
		{"source", func(d *store.NewDocument) { d.Source = "mail" }},
		{"original_sha256", func(d *store.NewDocument) { d.OriginalSHA256 = "" }},
		{"original_sha256", func(d *store.NewDocument) { d.OriginalSHA256 = strings.ToUpper(d.OriginalSHA256) }},
		{"original_sha256", func(d *store.NewDocument) { d.OriginalSHA256 = d.OriginalSHA256[:63] }},
		{"original_sha256", func(d *store.NewDocument) { d.OriginalSHA256 += "0" }},
		{"original_sha256", func(d *store.NewDocument) { d.OriginalSHA256 = "../" + d.OriginalSHA256[3:] }},
		{"original_size", func(d *store.NewDocument) { d.OriginalSize = -1 }},
		{"original_mime", func(d *store.NewDocument) { d.OriginalMIME = "" }},
		{"original_mime", func(d *store.NewDocument) { d.OriginalMIME = " " }},
		{"page_count", func(d *store.NewDocument) { d.PageCount = -1 }},
		{"archive_serial_number", func(d *store.NewDocument) { d.ArchiveSerialNumber = ptr(int32(0)) }},
		{"archive_size", func(d *store.NewDocument) { d.ArchiveSize = ptr(int64(1)) }},
		{"archive_sha256", func(d *store.NewDocument) { d.ArchiveSHA256, d.ArchiveSize = ptr("abc"), ptr(int64(1)) }},
		{"archive_size", func(d *store.NewDocument) { d.ArchiveSHA256, d.ArchiveSize = ptr(hashOf("pdf")), ptr(int64(-1)) }},
		{"title", func(d *store.NewDocument) { d.Title = "a\x00b" }},
		{"title", func(d *store.NewDocument) { d.Title = "\xff" }},
		{"notes", func(d *store.NewDocument) { d.Notes = "\x00" }},
		{"extracted_text", func(d *store.NewDocument) { d.ExtractedText = "\xc3" }},
		{"text_source", func(d *store.NewDocument) { d.TextSource = "\x00" }},
		{"original_file_name", func(d *store.NewDocument) { d.OriginalFileName = "a\x00.pdf" }},
	} {
		in := newDoc("valid")
		tc.change(&in)
		_, err := st.CreateDocument(ctx, alice, in)
		var verr *store.ValidationError
		if !errors.Is(err, store.ErrInvalid) || !errors.As(err, &verr) || verr.Field != tc.field {
			t.Errorf("%s: %v, want a ValidationError for %s", tc.field, err, tc.field)
		}
	}
	docs, err := st.ListDocuments(ctx, alice)
	if err != nil || len(docs) != 0 {
		t.Fatalf("documents after refused input: %d, %v", len(docs), err)
	}
	// All valid values still pass.
	in := newDoc("valid")
	in.Source, in.Title = store.SourceImage, "Grüße"
	in.ArchiveSHA256, in.ArchiveSize = ptr(hashOf("pdf")), ptr(int64(0))
	in.ArchiveSerialNumber = ptr(int32(1))
	mustDoc(t, st, alice, in)
}

// Check violations the store has no specific error for become a
// ValidationError, without SQL or the database's message.
func TestCheckViolationIsInvalid(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	for name, err := range map[string]error{
		"blank correspondent": func() error {
			_, err := st.CreateCorrespondent(ctx, alice, store.CorrespondentInput{Name: "  "})
			return err
		}(),
		"unknown match mode": func() error {
			_, err := st.CreateCorrespondent(ctx, alice, store.CorrespondentInput{Name: "x", MatchMode: "fuzzy"})
			return err
		}(),
		"bad colour": func() error {
			_, err := st.CreateTag(ctx, alice, store.TagInput{Name: "x", ColorHex: "red"})
			return err
		}(),
	} {
		var verr *store.ValidationError
		var pgErr *pgconn.PgError
		if !errors.Is(err, store.ErrInvalid) || !errors.As(err, &verr) || verr.Constraint == "" {
			t.Errorf("%s: %v, want a ValidationError naming the constraint", name, err)
			continue
		}
		if errors.As(err, &pgErr) {
			t.Errorf("%s: the PostgreSQL error is still reachable: %v", name, pgErr)
		}
		if msg := err.Error(); strings.Contains(msg, "SQLSTATE") || strings.Contains(msg, "violates") || strings.Contains(msg, verr.Constraint) {
			t.Errorf("%s: message %q exposes database details", name, msg)
		}
	}
}

// A new row whose owner does not exist gives ErrUnknownUser.
func TestUnknownOwner(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	ghost := store.Scope{UserID: 1 << 40}
	if _, err := st.CreateDocument(ctx, ghost, newDoc("a")); !errors.Is(err, store.ErrUnknownUser) {
		t.Errorf("document: %v, want ErrUnknownUser", err)
	}
	if _, err := st.CreateCorrespondent(ctx, ghost, store.CorrespondentInput{Name: "x"}); !errors.Is(err, store.ErrUnknownUser) {
		t.Errorf("correspondent: %v, want ErrUnknownUser", err)
	}
	if _, err := st.CreateTag(ctx, ghost, store.TagInput{Name: "x"}); !errors.Is(err, store.ErrUnknownUser) {
		t.Errorf("tag: %v, want ErrUnknownUser", err)
	}
}

// FindDocumentByOriginal looks only at the given owner's documents, also
// for an admin: another owner's document with the same file is never
// returned.
func TestFindDocumentByOriginal(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := storetest.User(t, st, "admin", true)

	in := newDoc("shared file")
	in.Title = "Alice's copy"
	a := mustDoc(t, st, alice, in)
	in.Title = "Bob's copy"
	b := mustDoc(t, st, bob, in)

	got, err := st.FindDocumentByOriginal(ctx, alice.UserID, hashOf("shared file"))
	if err != nil || got.ID != a.ID || got.Title != "Alice's copy" {
		t.Errorf("alice: %+v, %v, want %s", got, err, a.ID)
	}
	got, err = st.FindDocumentByOriginal(ctx, bob.UserID, hashOf("shared file"))
	if err != nil || got.ID != b.ID || got.Title != "Bob's copy" {
		t.Errorf("bob: %+v, %v, want %s", got, err, b.ID)
	}
	if got, err := st.FindDocumentByOriginal(ctx, admin.ID, hashOf("shared file")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("admin: %+v, %v, want ErrNotFound", got, err)
	}
	// So the admin's upload of the same file is a new document.
	if _, err := st.CreateDocument(ctx, store.Scope{UserID: admin.ID, All: true}, newDoc("shared file")); err != nil {
		t.Errorf("admin's copy: %v", err)
	}
	if _, err := st.FindDocumentByOriginal(ctx, alice.UserID, hashOf("other")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown hash: %v, want ErrNotFound", err)
	}
	for _, bad := range []string{"", "abc", strings.ToUpper(hashOf("shared file")), hashOf("shared file") + "0"} {
		if _, err := st.FindDocumentByOriginal(ctx, alice.UserID, bad); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("hash %q: %v, want ErrInvalid", bad, err)
		}
	}
}

func TestOwnershipSeparation(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "admin", true).ID, All: true}

	ad := mustDoc(t, st, alice, newDoc("alice doc"))
	ac := mustCorrespondent(t, st, alice, "Bank")
	at := mustTag(t, st, alice, "Tax", nil)
	aty, err := st.CreateDocumentType(ctx, alice, store.DocumentTypeInput{Name: "Letter"})
	if err != nil {
		t.Fatal(err)
	}
	mustDoc(t, st, bob, newDoc("bob doc"))
	mustCorrespondent(t, st, bob, "Insurance")
	mustTag(t, st, bob, "Car", nil)

	// Bob sees none of Alice's rows; they behave as missing.
	if docs, _ := st.ListDocuments(ctx, bob); len(docs) != 1 || docs[0].OwnerID != bob.UserID {
		t.Errorf("bob's documents = %+v", docs)
	}
	if cs, _ := st.ListCorrespondents(ctx, bob); len(cs) != 1 || cs[0].Name != "Insurance" {
		t.Errorf("bob's correspondents = %+v", cs)
	}
	if ts, _ := st.ListTags(ctx, bob); len(ts) != 1 || ts[0].Name != "Car" {
		t.Errorf("bob's tags = %+v", ts)
	}
	if ts, _ := st.ListDocumentTypes(ctx, bob); len(ts) != 0 {
		t.Errorf("bob's types = %+v", ts)
	}
	for name, err := range map[string]error{
		"get document":      second(st.GetDocument(ctx, bob, ad.ID)),
		"patch document":    second(st.PatchDocument(ctx, bob, ad.ID, store.DocumentPatch{Title: ptr("x")})),
		"set tags":          second(st.PatchDocument(ctx, bob, ad.ID, store.DocumentPatch{SetTags: true})),
		"delete document":   st.DeleteDocument(ctx, bob, ad.ID),
		"get correspondent": second(st.GetCorrespondent(ctx, bob, ac.ID)),
		"update corr.":      second(st.UpdateCorrespondent(ctx, bob, ac.ID, store.CorrespondentInput{Name: "x"})),
		"delete corr.":      st.DeleteCorrespondent(ctx, bob, ac.ID),
		"get tag":           second(st.GetTag(ctx, bob, at.ID)),
		"update tag":        second(st.UpdateTag(ctx, bob, at.ID, store.TagInput{Name: "x"})),
		"delete tag":        st.DeleteTag(ctx, bob, at.ID),
		"get type":          second(st.GetDocumentType(ctx, bob, aty.ID)),
		"update type":       second(st.UpdateDocumentType(ctx, bob, aty.ID, store.DocumentTypeInput{Name: "x"})),
		"delete type":       st.DeleteDocumentType(ctx, bob, aty.ID),
	} {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s across owners: %v, want ErrNotFound", name, err)
		}
	}
	if d, err := st.GetDocument(ctx, alice, ad.ID); err != nil || d.Title != "alice doc" {
		t.Errorf("alice's document changed: %+v, %v", d, err)
	}

	// The All scope sees and changes both owners' rows.
	if docs, _ := st.ListDocuments(ctx, admin); len(docs) != 2 {
		t.Errorf("admin sees %d documents, want 2", len(docs))
	}
	if cs, _ := st.ListCorrespondents(ctx, admin); len(cs) != 2 {
		t.Errorf("admin sees %d correspondents, want 2", len(cs))
	}
	if ts, _ := st.ListTags(ctx, admin); len(ts) != 2 {
		t.Errorf("admin sees %d tags, want 2", len(ts))
	}
	up, err := st.PatchDocument(ctx, admin, ad.ID, store.DocumentPatch{Title: ptr("by admin"), SetCorrespondent: true, CorrespondentID: &ac.ID})
	if err != nil || up.Title != "by admin" || up.OwnerID != alice.UserID {
		t.Errorf("admin update: %+v, %v", up, err)
	}
	if _, err := st.PatchDocument(ctx, admin, ad.ID, store.DocumentPatch{SetTags: true, TagIDs: []string{at.ID}}); err != nil {
		t.Errorf("admin sets alice's tag: %v", err)
	}
	// A document an admin creates is the admin's own.
	own := mustDoc(t, st, admin, newDoc("admin doc"))
	if own.OwnerID != admin.UserID {
		t.Errorf("admin's document owner = %d", own.OwnerID)
	}
}

func second[T any](_ T, err error) error { return err }

func TestOwnerMismatch(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "admin", true).ID, All: true}

	bc := mustCorrespondent(t, st, bob, "Bank")
	bt := mustTag(t, st, bob, "Tax", nil)
	bty, err := st.CreateDocumentType(ctx, bob, store.DocumentTypeInput{Name: "Letter"})
	if err != nil {
		t.Fatal(err)
	}
	missing := "00000000-0000-4000-8000-000000000000"
	ad := mustDoc(t, st, alice, newDoc("alice doc"))
	aliceTag := mustTag(t, st, alice, "Alice", nil)

	withCorr := newDoc("c")
	withCorr.CorrespondentID = &bc.ID
	withType := newDoc("t")
	withType.DocumentTypeID = &bty.ID
	withTag := newDoc("g")
	withTag.TagIDs = []string{bt.ID}
	withMissing := newDoc("m")
	withMissing.TagIDs = []string{missing}
	withMalformed := newDoc("x")
	withMalformed.CorrespondentID = ptr("nope")

	for name, err := range map[string]error{
		"create with other's correspondent": second(st.CreateDocument(ctx, alice, withCorr)),
		"create with other's type":          second(st.CreateDocument(ctx, alice, withType)),
		"create with other's tag":           second(st.CreateDocument(ctx, alice, withTag)),
		"create with missing tag":           second(st.CreateDocument(ctx, alice, withMissing)),
		"create with malformed link":        second(st.CreateDocument(ctx, alice, withMalformed)),
		"patch with other's correspondent":  second(st.PatchDocument(ctx, alice, ad.ID, store.DocumentPatch{SetCorrespondent: true, CorrespondentID: &bc.ID})),
		"admin links other owner's type":    second(st.PatchDocument(ctx, admin, ad.ID, store.DocumentPatch{SetDocumentType: true, DocumentTypeID: &bty.ID})),
		"set other's tag":                   second(st.PatchDocument(ctx, alice, ad.ID, store.DocumentPatch{SetTags: true, TagIDs: []string{bt.ID}})),
		"admin sets other owner's tag":      second(st.PatchDocument(ctx, admin, ad.ID, store.DocumentPatch{SetTags: true, TagIDs: []string{bt.ID}})),
		"tag under other's parent":          second(st.CreateTag(ctx, alice, store.TagInput{Name: "Sub", ParentID: &bt.ID})),
		"tag under missing parent":          second(st.CreateTag(ctx, alice, store.TagInput{Name: "Sub", ParentID: &missing})),
		"admin moves tag under other's tag": second(st.UpdateTag(ctx, admin, aliceTag.ID, store.TagInput{Name: "Alice", ParentID: &bt.ID})),
	} {
		if !errors.Is(err, store.ErrOwnerMismatch) {
			t.Errorf("%s: %v, want ErrOwnerMismatch", name, err)
		}
	}
	// Nothing of the failed calls was stored.
	if docs, _ := st.ListDocuments(ctx, alice); len(docs) != 1 {
		t.Errorf("alice has %d documents, want 1", len(docs))
	}
	if d, _ := st.GetDocument(ctx, alice, ad.ID); d.CorrespondentID != nil || len(d.TagIDs) != 0 {
		t.Errorf("document changed: %+v", d)
	}
}

func TestDeleteUserWithRowsFails(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := storetest.User(t, st, "alice", false)
	mustDoc(t, st, store.Scope{UserID: alice.ID}, newDoc("a"))
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, "DELETE FROM users WHERE id = $1", alice.ID)
	// ON DELETE RESTRICT: PostgreSQL 18 reports restrict_violation (23001),
	// older versions foreign_key_violation (23503).
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || (pgErr.Code != "23001" && pgErr.Code != "23503") {
		t.Fatalf("delete user with documents: %v, want a restrict or foreign key violation", err)
	}
}

func TestCreateUser(t *testing.T) {
	st := storetest.New(t)
	u := storetest.User(t, st, "alice", true)
	if u.ID <= 0 || u.Name != "alice" || !u.IsAdmin || u.CreatedAt.IsZero() {
		t.Errorf("user = %+v", u)
	}
	if _, err := st.CreateUser(context.Background(), "alice", "hash", false); !errors.Is(err, store.ErrNameTaken) {
		t.Errorf("second alice: %v, want ErrNameTaken", err)
	}
	if _, err := st.CreateDocument(context.Background(), store.Scope{All: true}, newDoc("a")); err == nil {
		t.Error("a scope without user created a document")
	}
}

func TestDuplicatesPerOwner(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}

	first := mustDoc(t, st, alice, newDoc("same file"))
	again := newDoc("same file")
	again.Title = "another title"
	_, err := st.CreateDocument(ctx, alice, again)
	var dup *store.DuplicateError
	if !errors.Is(err, store.ErrDuplicate) || !errors.As(err, &dup) || dup.ExistingID != first.ID {
		t.Fatalf("duplicate: %v, want DuplicateError{%s}", err, first.ID)
	}
	// The same file for another owner is not a duplicate, and nothing
	// tells Bob that Alice has it.
	if d, err := st.CreateDocument(ctx, bob, newDoc("same file")); err != nil || d.ID == first.ID {
		t.Fatalf("same file for bob: %+v, %v", d, err)
	}
}

func TestASNUniquePerOwner(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}

	a := newDoc("a")
	a.ArchiveSerialNumber = ptr(int32(1))
	mustDoc(t, st, alice, a)
	b := newDoc("b")
	b.ArchiveSerialNumber = ptr(int32(1))
	if _, err := st.CreateDocument(ctx, alice, b); !errors.Is(err, store.ErrASNTaken) {
		t.Errorf("same ASN for alice: %v, want ErrASNTaken", err)
	}
	mustDoc(t, st, bob, b)
	// Documents without an ASN do not collide.
	mustDoc(t, st, alice, newDoc("c"))
	d := mustDoc(t, st, alice, newDoc("d"))
	if _, err := st.PatchDocument(ctx, alice, d.ID, store.DocumentPatch{SetASN: true, ArchiveSerialNumber: ptr(int32(1))}); !errors.Is(err, store.ErrASNTaken) {
		t.Errorf("update to a taken ASN: %v, want ErrASNTaken", err)
	}
	zero := newDoc("e")
	zero.ArchiveSerialNumber = ptr(int32(0))
	if _, err := st.CreateDocument(ctx, alice, zero); err == nil {
		t.Error("ASN 0 was accepted")
	}
}

func TestDeleteMetadataKeepsDocuments(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	corr := mustCorrespondent(t, st, alice, "Bank")
	typ, err := st.CreateDocumentType(ctx, alice, store.DocumentTypeInput{Name: "Letter"})
	if err != nil {
		t.Fatal(err)
	}
	keep := mustTag(t, st, alice, "Keep", nil)
	gone := mustTag(t, st, alice, "Gone", nil)
	in := newDoc("a")
	in.CorrespondentID = &corr.ID
	in.DocumentTypeID = &typ.ID
	in.TagIDs = []string{keep.ID, gone.ID}
	d := mustDoc(t, st, alice, in)

	if err := st.DeleteCorrespondent(ctx, alice, corr.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteDocumentType(ctx, alice, typ.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTag(ctx, alice, gone.ID); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetDocument(ctx, alice, d.ID)
	if err != nil {
		t.Fatalf("document gone with its metadata: %v", err)
	}
	if got.CorrespondentID != nil || got.DocumentTypeID != nil || len(got.TagIDs) != 1 || got.TagIDs[0] != keep.ID {
		t.Errorf("document after deletes = %+v", got)
	}
	if got.OwnerID != alice.UserID {
		t.Errorf("owner changed to %d", got.OwnerID)
	}
}

func TestDeleteDocumentCascadesToTagLinks(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	tag := mustTag(t, st, alice, "Tax", nil)
	in := newDoc("a")
	in.TagIDs = []string{tag.ID}
	d := mustDoc(t, st, alice, in)
	if err := st.DeleteDocument(ctx, alice, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetTag(ctx, alice, tag.ID); err != nil {
		t.Errorf("tag gone with the document: %v", err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM document_tags").Scan(&n); err != nil || n != 0 {
		t.Errorf("document_tags rows = %d (%v), want 0", n, err)
	}
}
