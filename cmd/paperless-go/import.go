package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/ingest"
	"github.com/pklnx/paperless-go/internal/store"
)

func newImportCmd() *cobra.Command {
	var userName string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "import PATH [--user NAME] [--dry-run]",
		Short: "Import the files in a folder as documents",
		Long: `Import PATH, a file or a folder (searched recursively), as new documents in
the inbox of a user. PDF, JPEG, PNG and HEIC/HEIF files are accepted; the type
is decided from the content, not the name. Symbolic links and other special
files are skipped. The source files are never changed, moved or deleted.

One line per file says imported, duplicate (the user already has the file, with
the existing title) or failed (with the reason), followed by a summary. The
exit status is 1 if any file failed, so running the command again after fixing
the files is safe: files already imported are reported as duplicates.

--user is required when there is more than one user; a locked user is refused.
--dry-run checks every file (type, size, pixel limit, duplicates) and stores
nothing. Files larger than PAPERLESS_GO_MAX_UPLOAD_SIZE are refused.

With ./pgo, DIR is a folder on the host: ./pgo import DIR.`,
		Example: `  paperless-go import /srv/scans --user alice
  ./pgo import ~/Documents/scans --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(cmd, args[0], userName, dryRun)
		},
	}
	cmd.Flags().StringVar(&userName, "user", "", "the user who owns the documents (required if there are several users)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "check the files and store nothing")
	return cmd
}

// importTally counts the outcomes of an import run.
type importTally struct{ imported, duplicates, failed int }

func runImport(cmd *cobra.Command, path, userName string, dryRun bool) error {
	root, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", path, unwrapPathError(err))
	}
	if _, err := os.Stat(root); err != nil {
		return fmt.Errorf("cannot read %s: %w", path, unwrapPathError(err))
	}
	a, err := openApp(cmd.Context())
	if err != nil {
		return err
	}
	defer a.close()
	owner, err := importOwner(cmd.Context(), a.store, userName)
	if err != nil {
		return err
	}
	if a.cfg.MaxUploadSize <= 0 {
		return errors.New("the size limit must be positive")
	}

	imp := &importer{
		ctx: cmd.Context(), out: cmd.OutOrStdout(), st: a.store, owner: owner,
		dryRun: dryRun, maxSize: a.cfg.MaxUploadSize,
	}
	if !dryRun {
		blobs, err := blobstore.New(a.cfg.DataDir)
		if err != nil {
			return fmt.Errorf("open the document files: %w", err)
		}
		if imp.im, err = ingest.New(a.store, blobs, a.cfg.MaxUploadSize); err != nil {
			return err
		}
	}

	walkErr := imp.walk(root, path)
	t := imp.tally
	verb := "imported"
	if dryRun {
		verb = "would import"
	}
	fmt.Fprintf(imp.out, "%s: %d, duplicates: %d, failed: %d\n", verb, t.imported, t.duplicates, t.failed)

	if !dryRun && t.imported > 0 {
		// One summary entry per run, written after the documents (the
		// documents are written one by one, each in its own transaction).
		// An interrupted run still records what it imported, so the entry
		// does not use the cancelled context.
		e := cliEntry(cmd, "document.import")
		e.SetTarget(store.TargetUser, strconv.FormatInt(owner.ID, 10), owner.Name)
		e.Detail("path", path)
		e.Detail("imported", t.imported)
		e.Detail("duplicates", t.duplicates)
		e.Detail("failed", t.failed)
		if walkErr != nil {
			e.Detail("interrupted", true)
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 10*time.Second)
		defer cancel()
		if err := a.store.Audit(ctx, *e); err != nil {
			return fmt.Errorf("%w; the documents were imported", err)
		}
	}
	if walkErr != nil {
		return walkErr
	}
	if t.failed > 0 {
		return &exitError{code: 1, msg: fmt.Sprintf("%d of %d files failed", t.failed, t.imported+t.duplicates+t.failed)}
	}
	return nil
}

func unwrapPathError(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// importOwner picks the user who owns the imported documents.
func importOwner(ctx context.Context, st *store.Store, name string) (*store.User, error) {
	var u *store.User
	if name != "" {
		name = auth.NormalizeUserName(name)
		var err error
		if u, err = st.GetUserByName(ctx, name); errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("unknown user %q", name)
		} else if err != nil {
			return nil, err
		}
	} else {
		users, err := st.ListUsers(ctx)
		if err != nil {
			return nil, err
		}
		switch len(users) {
		case 0:
			return nil, errors.New("there are no users; create one with: " + commandName() + " user add NAME")
		case 1:
			u = users[0]
		default:
			return nil, errors.New("there are several users; name the owner with --user NAME")
		}
	}
	if u.LockedAt != nil {
		return nil, fmt.Errorf("user %q is locked; unlock the user first", u.Name)
	}
	return u, nil
}

type importer struct {
	ctx     context.Context
	out     io.Writer
	st      *store.Store
	im      *ingest.Importer // nil for --dry-run
	owner   *store.User
	dryRun  bool
	maxSize int64
	tally   importTally
}

// walk imports every regular file below root (or root itself, if it is
// one) in lexical order. Symbolic links and special files are skipped.
// Names are printed relative to root.
func (m *importer) walk(root, shown string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if cerr := m.ctx.Err(); cerr != nil {
			return cerr
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			rel = filepath.Base(shown)
		}
		if err != nil {
			// An unreadable entry: report it and go on.
			m.fail(rel, unwrapPathError(err))
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // directories are walked; links and special files skipped
		}
		m.file(p, rel, d.Name())
		return nil
	})
}

func (m *importer) fail(rel string, reason error) {
	m.tally.failed++
	fmt.Fprintf(m.out, "%s: failed: %s\n", rel, reason)
}

func (m *importer) file(p, rel, name string) {
	f, err := os.Open(p) //nolint:gosec // the path is the one the operator named
	if err != nil {
		m.fail(rel, unwrapPathError(err))
		return
	}
	defer f.Close()
	// A file replaced by a link or special file between listing and open
	// is not followed: check what was opened.
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return
	}
	if m.dryRun {
		m.check(f, rel)
		return
	}
	res, err := m.im.Import(m.ctx, ingest.Request{OwnerID: m.owner.ID, FileName: name, Body: f})
	switch {
	case err != nil:
		m.fail(rel, err)
	case res.Outcome == ingest.Imported:
		m.tally.imported++
		fmt.Fprintf(m.out, "%s: imported\n", rel)
	case res.Outcome == ingest.Duplicate:
		m.duplicate(rel, res.Existing.Title)
	default:
		m.fail(rel, refusalText(res.Refusal))
	}
}

func (m *importer) duplicate(rel, title string) {
	m.tally.duplicates++
	if title == "" {
		fmt.Fprintf(m.out, "%s: duplicate\n", rel)
		return
	}
	fmt.Fprintf(m.out, "%s: duplicate of %q\n", rel, title)
}

// check is the --dry-run version of Import: the same type, size and pixel
// checks and the duplicate lookup, but nothing is stored.
func (m *importer) check(f *os.File, rel string) {
	h := sha256.New()
	lr := io.LimitReader(f, m.maxSize+1)
	prefix := make([]byte, ingest.PrefixSize)
	n, err := io.ReadFull(io.TeeReader(lr, h), prefix)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		m.fail(rel, err)
		return
	}
	if _, err := io.Copy(h, lr); err != nil {
		m.fail(rel, err)
		return
	}
	if fi, err := f.Stat(); err == nil && fi.Size() > m.maxSize {
		m.fail(rel, refusalText(&ingest.RefusedError{Reason: ingest.ReasonTooLarge, Detail: fmt.Sprintf("larger than %d bytes", m.maxSize)}))
		return
	}
	if _, err := ingest.Detect(prefix[:n]); err != nil {
		var r *ingest.RefusedError
		if errors.As(err, &r) {
			m.fail(rel, refusalText(r))
			return
		}
		m.fail(rel, err)
		return
	}
	ref, err := m.st.FindDocumentByOriginal(m.ctx, m.owner.ID, hex.EncodeToString(h.Sum(nil)))
	switch {
	case err == nil:
		m.duplicate(rel, ref.Title)
	case errors.Is(err, store.ErrNotFound):
		m.tally.imported++
		fmt.Fprintf(m.out, "%s: would import\n", rel)
	default:
		m.fail(rel, err)
	}
}

// refusalText is the reason of a refused file in plain words.
func refusalText(r *ingest.RefusedError) error {
	var s string
	switch r.Reason {
	case ingest.ReasonUnsupported:
		s = "unsupported file type"
	case ingest.ReasonTooLarge:
		s = "file too large"
	case ingest.ReasonTooManyPixels:
		s = "image has too many pixels"
	case ingest.ReasonUnreadable:
		s = "file cannot be read as its type"
	default:
		s = string(r.Reason)
	}
	if r.Detail != "" {
		s += " (" + strings.TrimSpace(r.Detail) + ")"
	}
	return errors.New(s)
}
