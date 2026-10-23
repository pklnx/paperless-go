package main

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/config"
	"github.com/pklnx/paperless-go/internal/ingest"
	"github.com/pklnx/paperless-go/internal/store"
)

// seedDoc is a generated document of the demo. Each has a word no other
// document has (word), so a search for it finds exactly this document;
// web/e2e/demo.ts names the ones the tests use. The exceptions are the
// groups that share one word on purpose (bulkDocs, pairDocs): a search for
// it finds the whole group.
type seedDoc struct {
	owner         string
	title         string
	date          string // YYYY-MM-DD
	correspondent string
	docType       string
	tags          []string
	inbox         bool
	favorite      bool
	word          string
	// pages are the lines of each page; the word is added to the last line
	// of the first page.
	pages [][]string
}

// The tags of demo: name and parent.
var seedTags = []struct{ name, parent string }{
	{"Finance", ""},
	{"Bills", "Finance"},
	{"Taxes", "Finance"},
	{"Home", ""},
	// The bulk edit test adds it.
	{"Review", ""},
}

var (
	// Meridian Bank and Statement are set by the bulk edit test.
	seedCorrespondents = []string{"City Utilities", "Tax Office", "Northwind Insurance", "Hausverwaltung Berg", "Meridian Bank"}
	seedTypes          = []string{"Invoice", "Letter", "Contract", "Statement"}
)

// bulkBatch is how many documents share bulkWord: more than one page of the
// list (the web UI loads 50), so that "Select all matches of this search"
// is offered.
const (
	bulkBatch = 52
	bulkWord  = "bulkbatch"
	pairWord  = "bulkpair"
)

// workDocs are the documents the end-to-end specs of bulk edit, trash and
// metadata change. Each spec has its own, so that no spec depends on
// another or changes what the rest of the suite looks at. They are
// imported before all others and dated early, so that the list (newest
// added first) shows them last: the first page is still demo's own
// documents. web/e2e/demo.ts names them (seed.bulkPair, seed.trashUndo…);
// trashAgain's lines are repeated there to build the same file.
func workDocs() []seedDoc {
	docs := make([]seedDoc, 0, bulkBatch+8)
	for i := 1; i <= bulkBatch; i++ {
		docs = append(docs, seedDoc{
			owner: "demo", title: fmt.Sprintf("Bulk batch %02d", i), date: "2023-03-01", word: bulkWord,
			pages: [][]string{{"Sample for the bulk edit", fmt.Sprintf("Number %02d of the batch.", i)}},
		})
	}
	return append(docs,
		seedDoc{
			owner: "demo", title: "Bulk pair one", date: "2023-02-01", word: pairWord,
			pages: [][]string{{"First of two documents", "They are changed together."}},
		},
		seedDoc{
			owner: "demo", title: "Bulk pair two", date: "2023-02-02", word: pairWord,
			pages: [][]string{{"Second of two documents", "They are changed together."}},
		},
		seedDoc{
			owner: "demo", title: "Trash flow undo", date: "2023-01-03", word: "trashundo",
			pages: [][]string{{"Moved to the trash and back", "Undo, then the trash view."}},
		},
		seedDoc{
			owner: "demo", title: "Trash flow purge", date: "2023-01-02", word: "trashpurge",
			pages: [][]string{{"Moved to the trash and removed", "Deleted permanently."}},
		},
		seedDoc{
			owner: "demo", title: "Trash flow upload again", date: "2023-01-01", word: "trashagain",
			pages: [][]string{{"Moved to the trash, then uploaded again", "The same file."}},
		},
		seedDoc{
			owner: "demo", title: "Metadata sample", date: "2022-12-01", word: "metasample",
			pages: [][]string{{"Gets a new tag, correspondent and type", "The metadata page changes them."}},
		},
	)
}

// seedDocs are imported in this order; the list shows the newest first.
var seedDocs = []seedDoc{
	{
		owner: "demo", title: "Electricity bill October 2026", date: "2026-10-05",
		correspondent: "City Utilities", docType: "Invoice", tags: []string{"Bills"}, word: "amberlight",
		pages: [][]string{{"City Utilities", "Invoice 2026-10 for electricity", "Amount due: 84.20 EUR, charged on October 15."}},
	},
	{
		owner: "demo", title: "Water bill September 2026", date: "2026-09-12",
		correspondent: "City Utilities", docType: "Invoice", tags: []string{"Bills"}, word: "bluefinch",
		pages: [][]string{{"City Utilities", "Invoice 2026-09 for water", "Amount due: 31.70 EUR."}},
	},
	{
		owner: "demo", title: "Income tax assessment 2025", date: "2026-07-21",
		correspondent: "Tax Office", docType: "Letter", tags: []string{"Taxes"}, word: "copperleaf",
		pages: [][]string{{"Tax Office", "Assessment of income tax for 2025", "The refund of 412.00 EUR is paid within two weeks."}},
	},
	{
		owner: "demo", title: "Property tax notice 2026", date: "2026-02-15",
		correspondent: "Tax Office", docType: "Letter", tags: []string{"Taxes"}, word: "duskmoor",
		pages: [][]string{{"Tax Office", "Property tax for 2026", "Four instalments of 96.50 EUR each."}},
	},
	{
		owner: "demo", title: "Home insurance policy", date: "2026-01-10",
		correspondent: "Northwind Insurance", docType: "Contract", tags: []string{"Finance", "Home"}, favorite: true, word: "emberglow",
		pages: [][]string{{"Northwind Insurance", "Policy for the household contents", "Annual premium: 148.00 EUR."}},
	},
	{
		owner: "demo", title: "Car insurance renewal", date: "2026-03-30",
		correspondent: "Northwind Insurance", docType: "Letter", tags: []string{"Finance"}, word: "frostvale",
		pages: [][]string{{"Northwind Insurance", "Your car insurance renews on May 1.", "The new premium is 512.40 EUR a year."}},
	},
	{
		owner: "demo", title: "Apartment lease", date: "2025-11-01",
		correspondent: "Hausverwaltung Berg", docType: "Contract", tags: []string{"Home"}, word: "glimmerbay",
		pages: [][]string{
			{"Lease agreement for the apartment at Lindenallee 4", "Between Hausverwaltung Berg and the tenant."},
			{"Page two: the rent is 980.00 EUR a month.", "Signed by both parties."},
		},
	},
	{
		owner: "demo", title: "Kündigung des Mietvertrags", date: "2026-08-31",
		correspondent: "Hausverwaltung Berg", docType: "Letter", tags: []string{"Home"}, word: "ironquill",
		pages: [][]string{{"Kündigung des Mietvertrags zum 30. November", "Sehr geehrte Damen und Herren, hiermit kündige ich das Mietverhältnis.", "Mit freundlichen Grüßen"}},
	},
	{
		owner: "demo", title: "Internet bill August 2026", date: "2026-08-03",
		docType: "Invoice", tags: []string{"Bills"}, word: "juniperfox",
		pages: [][]string{{"Fibre line, August 2026", "Amount due: 39.99 EUR."}},
	},
	{
		owner: "demo", title: "Dentist appointment reminder", date: "2026-06-18",
		docType: "Letter", word: "kestrelwind",
		pages: [][]string{{"Dental practice", "Your check-up is on June 30 at 9:00."}},
	},
	{
		owner: "demo", title: "Warranty card for the washing machine", date: "2024-05-02", word: "lanternbrook",
		pages: [][]string{{"Warranty card", "Two years of warranty from the day of purchase."}},
	},
	{
		owner: "demo", title: "Letter from the bank", date: "2026-10-20", inbox: true, word: "mossharbor",
		pages: [][]string{{"Your bank", "New terms and conditions from January 1."}},
	},
	// Another user's documents, for owner separation.
	{
		owner: "max", title: "Gym membership", date: "2026-04-01", word: "nettlecrest",
		pages: [][]string{{"City Gym", "Membership from April 1, 29.00 EUR a month."}},
	},
	{
		owner: "max", title: "Phone contract", date: "2026-05-15", word: "opalridge",
		pages: [][]string{{"Phone company", "Your contract runs for 24 months."}},
	},
}

// seed adds demo's metadata and imports the documents (queued for
// processing), with their fields set as a user would.
func seed(ctx context.Context, st *store.Store, blobs *blobstore.Store, ids map[string]int64) error {
	owner := store.Scope{UserID: ids["demo"]}
	tags := map[string]string{}
	for _, t := range seedTags {
		in := store.TagInput{Name: t.name}
		if t.parent != "" {
			parent := tags[t.parent]
			in.ParentID = &parent
		}
		row, err := st.CreateTag(ctx, owner, in)
		if err != nil {
			return fmt.Errorf("tag %s: %w", t.name, err)
		}
		tags[t.name] = row.ID
	}
	corrs := map[string]string{}
	for _, name := range seedCorrespondents {
		row, err := st.CreateCorrespondent(ctx, owner, store.CorrespondentInput{Name: name})
		if err != nil {
			return fmt.Errorf("correspondent %s: %w", name, err)
		}
		corrs[name] = row.ID
	}
	types := map[string]string{}
	for _, name := range seedTypes {
		row, err := st.CreateDocumentType(ctx, owner, store.DocumentTypeInput{Name: name})
		if err != nil {
			return fmt.Errorf("document type %s: %w", name, err)
		}
		types[name] = row.ID
	}

	im, err := ingest.New(st, blobs, config.DefaultMaxUploadSize)
	if err != nil {
		return err
	}
	for _, d := range append(workDocs(), seedDocs...) {
		pages := make([][]string, len(d.pages))
		copy(pages, d.pages)
		pages[0] = append(append([]string{}, d.pages[0]...), "Reference word: "+d.word)
		pdf, err := makePDF(pages)
		if err != nil {
			return err
		}
		sc := store.Scope{UserID: ids[d.owner]}
		res, err := im.Import(ctx, ingest.Request{OwnerID: sc.UserID, FileName: d.word + ".pdf", Title: d.title, Body: bytes.NewReader(pdf)})
		if err != nil {
			return fmt.Errorf("import %s: %w", d.title, err)
		}
		if res.Outcome != ingest.Imported {
			return fmt.Errorf("import %s: %s", d.title, res.Outcome)
		}
		date, err := time.Parse(time.DateOnly, d.date)
		if err != nil {
			return err
		}
		p := store.DocumentPatch{DocumentDate: &date, IsInbox: &d.inbox, IsFavorite: &d.favorite}
		if d.correspondent != "" {
			id := corrs[d.correspondent]
			p.SetCorrespondent, p.CorrespondentID = true, &id
		}
		if d.docType != "" {
			id := types[d.docType]
			p.SetDocumentType, p.DocumentTypeID = true, &id
		}
		if len(d.tags) > 0 {
			p.SetTags = true
			for _, name := range d.tags {
				p.TagIDs = append(p.TagIDs, tags[name])
			}
		}
		if _, err := st.PatchDocument(ctx, sc, res.Document.ID, p); err != nil {
			return fmt.Errorf("fields of %s: %w", d.title, err)
		}
	}
	return nil
}
