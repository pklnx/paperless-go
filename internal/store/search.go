package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pklnx/paperless-go/internal/search"
	"github.com/pklnx/paperless-go/internal/store/db"
)

var (
	// ErrInvalidCursor means a search cursor is malformed or comes from
	// another sort order.
	ErrInvalidCursor = errors.New("store: invalid cursor")
	// ErrInvalidSort means a sort the store does not offer, such as
	// relevance in ascending order.
	ErrInvalidSort = errors.New("store: invalid sort")
)

// SearchHit is one document of a search result. ExtractedText is empty;
// Snippet holds up to two fragments of the text around the matches, each
// match between SnippetStart and SnippetStop. It is empty without free
// text.
type SearchHit struct {
	*Document
	Snippet string
}

// Markers around a match in SearchHit.Snippet: characters of Unicode's
// private use area, which a document's text does not use. A client splits
// the snippet at them and never treats it as HTML.
const (
	SnippetStart = ""
	SnippetStop  = ""
)

// DocumentPage is one page of a search.
type DocumentPage struct {
	Hits []SearchHit
	// NextCursor continues after this page (search.Query.Cursor); it is
	// empty on the last page.
	NextCursor string
}

// unnamedStatements runs every query as an unnamed statement, so
// PostgreSQL plans it with its values: the NULL tests of unused search
// filters fold away, and the full-text condition can use its index. A
// prepared statement may switch to a generic plan after a few runs,
// which cannot do either.
type unnamedStatements struct{ db.DBTX }

func (u unnamedStatements) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return u.DBTX.Exec(ctx, sql, append([]any{pgx.QueryExecModeCacheDescribe}, args...)...)
}

func (u unnamedStatements) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return u.DBTX.Query(ctx, sql, append([]any{pgx.QueryExecModeCacheDescribe}, args...)...)
}

func (u unnamedStatements) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return u.DBTX.QueryRow(ctx, sql, append([]any{pgx.QueryExecModeCacheDescribe}, args...)...)
}

// searchCursor is the decoded form of DocumentPage.NextCursor: the sort
// order and the last row's key and id. A relevance key is kept as the
// exact bits of the float.
type searchCursor struct {
	Sort      search.Sort `json:"s"`
	Ascending bool        `json:"a,omitempty"`
	At        *int64      `json:"t,omitempty"` // added or date, Unix microseconds
	Rank      *uint32     `json:"r,omitempty"`
	ID        string      `json:"i"`
}

func encodeCursor(c searchCursor) string {
	b, _ := json.Marshal(c) // a plain struct cannot fail
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string, sort search.Sort, asc bool) (searchCursor, pgtype.UUID, error) {
	var c searchCursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) > 256 {
		return c, pgtype.UUID{}, ErrInvalidCursor
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, pgtype.UUID{}, ErrInvalidCursor
	}
	id, ok := parseID(c.ID)
	if !ok || c.Sort != sort || c.Ascending != asc {
		return c, pgtype.UUID{}, ErrInvalidCursor
	}
	if (sort == search.SortRelevance) != (c.Rank != nil) || (sort == search.SortRelevance) == (c.At != nil) {
		return c, pgtype.UUID{}, ErrInvalidCursor
	}
	return c, id, nil
}

// searchArgs are the parameters of the search queries, shared by all sort
// orders.
type searchArgs = db.SearchDocumentsAddedDescParams

// SearchDocuments returns one page of the scope's documents that match q,
// without their extracted text. Without free text or filters it lists the
// newest documents. Free text comes with snippets.
//
// The sort is q.Sort, by default relevance with free text and newest added
// otherwise; relevance falls back to newest added when no free-text item
// is left (only stop words or punctuation). Errors: ErrInvalidCursor,
// ErrInvalidSort, and a *ValidationError (ErrInvalid) for text that is not
// valid UTF-8 or holds NUL, or a malformed ID.
func (s *Store) SearchDocuments(ctx context.Context, sc Scope, q search.Query) (*DocumentPage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = search.DefaultLimit
	}
	limit = min(limit, search.MaxLimit)
	if _, ok := search.ParseSort(string(q.Sort)); !ok || q.Sort == search.SortRelevance && q.Ascending {
		return nil, ErrInvalidSort
	}

	args := searchArgs{Owner: sc.owner(), Lim: clampInt32(limit + 1)}
	if q.HasText() {
		tsq, err := s.searchTsquery(ctx, sc, q.Text)
		if err != nil {
			return nil, err
		}
		if tsq != "" {
			args.Query = tsq
		}
	}

	sort := q.Sort
	switch {
	case sort == "" && args.Query != nil && !q.Ascending:
		sort = search.SortRelevance
	case sort == "" || sort == search.SortRelevance && args.Query == nil:
		sort = search.SortAdded
	}

	var cursorRank *float32
	if q.Cursor != "" {
		c, id, err := decodeCursor(q.Cursor, sort, q.Ascending)
		if err != nil {
			return nil, err
		}
		args.CursorID = id
		if c.Rank != nil {
			r := math.Float32frombits(*c.Rank)
			cursorRank = &r
		} else {
			t := time.UnixMicro(*c.At).UTC()
			args.CursorAt = &t
		}
	}

	none, err := s.searchFilters(ctx, sc, q, &args)
	if err != nil {
		return nil, err
	}
	if none {
		return &DocumentPage{Hits: []SearchHit{}}, nil
	}

	var rows []db.SearchDocumentsAddedDescRow
	switch {
	case sort == search.SortRelevance:
		r, err := s.sq.SearchDocumentsRelevance(ctx, db.SearchDocumentsRelevanceParams{
			Query: args.Query, Owner: args.Owner, TagGroupCount: args.TagGroupCount, TagGroups: args.TagGroups,
			TagIds: args.TagIds, NotTagIds: args.NotTagIds, Correspondents: args.Correspondents,
			NotCorrespondents: args.NotCorrespondents, Types: args.Types, NotTypes: args.NotTypes, Asns: args.Asns,
			NotAsns: args.NotAsns, DateFrom: args.DateFrom, DateTo: args.DateTo, NotDateFrom: args.NotDateFrom,
			NotDateTo: args.NotDateTo, AddedFrom: args.AddedFrom, AddedTo: args.AddedTo,
			NotAddedFrom: args.NotAddedFrom, NotAddedTo: args.NotAddedTo, Inbox: args.Inbox,
			Favorite: args.Favorite, CursorRank: cursorRank, CursorID: args.CursorID, Lim: args.Lim,
		})
		err = convertRows(&rows, r, err, func(x db.SearchDocumentsRelevanceRow) db.SearchDocumentsAddedDescRow {
			return db.SearchDocumentsAddedDescRow(x)
		})
		if err != nil {
			return nil, err
		}
	case sort == search.SortAdded && !q.Ascending:
		if rows, err = s.sq.SearchDocumentsAddedDesc(ctx, args); err != nil {
			return nil, err
		}
	case sort == search.SortAdded:
		r, err := s.sq.SearchDocumentsAddedAsc(ctx, db.SearchDocumentsAddedAscParams(args))
		if err := convertRows(&rows, r, err, func(x db.SearchDocumentsAddedAscRow) db.SearchDocumentsAddedDescRow {
			return db.SearchDocumentsAddedDescRow(x)
		}); err != nil {
			return nil, err
		}
	case !q.Ascending:
		r, err := s.sq.SearchDocumentsDateDesc(ctx, db.SearchDocumentsDateDescParams(args))
		if err := convertRows(&rows, r, err, func(x db.SearchDocumentsDateDescRow) db.SearchDocumentsAddedDescRow {
			return db.SearchDocumentsAddedDescRow(x)
		}); err != nil {
			return nil, err
		}
	default:
		r, err := s.sq.SearchDocumentsDateAsc(ctx, db.SearchDocumentsDateAscParams(args))
		if err := convertRows(&rows, r, err, func(x db.SearchDocumentsDateAscRow) db.SearchDocumentsAddedDescRow {
			return db.SearchDocumentsAddedDescRow(x)
		}); err != nil {
			return nil, err
		}
	}

	page := &DocumentPage{Hits: make([]SearchHit, 0, min(len(rows), limit))}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			c := searchCursor{Sort: sort, Ascending: q.Ascending, ID: idString(last.ID)}
			switch sort {
			case search.SortRelevance:
				bits := math.Float32bits(last.Rank)
				c.Rank = &bits
			case search.SortAdded:
				at := last.CreatedAt.UnixMicro()
				c.At = &at
			default:
				at := last.DocumentDate.UnixMicro()
				c.At = &at
			}
			page.NextCursor = encodeCursor(c)
			break
		}
		page.Hits = append(page.Hits, SearchHit{Document: documentFromDB(db.GetDocumentRow{
			ID: r.ID, OwnerID: r.OwnerID, Title: r.Title, DocumentDate: r.DocumentDate, CreatedAt: r.CreatedAt,
			ModifiedAt: r.ModifiedAt, Notes: r.Notes, IsFavorite: r.IsFavorite, IsInbox: r.IsInbox,
			ArchiveSerialNumber: r.ArchiveSerialNumber, PageCount: r.PageCount, ExtractedText: r.ExtractedText,
			TextSource: r.TextSource, Source: r.Source, OriginalFileName: r.OriginalFileName,
			OriginalSha256: r.OriginalSha256, OriginalSize: r.OriginalSize, OriginalMime: r.OriginalMime,
			ArchiveSha256: r.ArchiveSha256, ArchiveSize: r.ArchiveSize, CorrespondentID: r.CorrespondentID,
			DocumentTypeID: r.DocumentTypeID, ThumbnailSha256: r.ThumbnailSha256, ProcessingNote: r.ProcessingNote,
			TagIds: r.TagIds,
		}), Snippet: r.Snippet})
	}
	return page, nil
}

func convertRows[T any](dst *[]db.SearchDocumentsAddedDescRow, src []T, err error, conv func(T) db.SearchDocumentsAddedDescRow) error {
	if err != nil {
		return err
	}
	*dst = make([]db.SearchDocumentsAddedDescRow, len(src))
	for i, r := range src {
		(*dst)[i] = conv(r)
	}
	return nil
}

// searchTsquery builds the tsquery of the free-text items once, before
// the search: as a bound constant it uses the index, where a call of
// document_search_tsquery in the search query would run for every row of
// a sequential scan. "" means no item is left.
func (s *Store) searchTsquery(ctx context.Context, sc Scope, items []search.Text) (string, error) {
	p := db.SearchTsqueryParams{Owner: sc.owner()}
	for _, it := range items {
		if !validText(it.Value) {
			return "", invalid("q")
		}
		p.Terms = append(p.Terms, it.Value)
		p.IsPhrase = append(p.IsPhrase, it.Phrase)
		p.Negated = append(p.Negated, it.Negated)
		p.OrGroup = append(p.OrGroup, clampInt32(it.Group))
	}
	return s.sq.SearchTsquery(ctx, p)
}

// idSet is a set of UUIDs in insertion order.
type idSet struct {
	ids  []pgtype.UUID
	seen map[[16]byte]bool
}

func (s *idSet) add(ids ...pgtype.UUID) {
	if s.seen == nil {
		s.seen = map[[16]byte]bool{}
	}
	for _, id := range ids {
		if !s.seen[id.Bytes] {
			s.seen[id.Bytes] = true
			s.ids = append(s.ids, id)
		}
	}
}

// list returns the IDs, never nil: an empty positive set matches nothing.
func (s *idSet) list() []pgtype.UUID {
	if s.ids == nil {
		return []pgtype.UUID{}
	}
	return s.ids
}

// intersect narrows a positive set (nil: no condition yet) by another.
func intersect(cur *idSet, next idSet) *idSet {
	if cur == nil {
		return &next
	}
	var out idSet
	for _, id := range cur.ids {
		if next.seen[id.Bytes] {
			out.add(id)
		}
	}
	if out.seen == nil {
		out.seen = map[[16]byte]bool{}
	}
	return &out
}

func parseIDs(field string, ids []string) (idSet, error) {
	var set idSet
	for _, v := range ids {
		u, ok := parseID(v)
		if !ok {
			return idSet{}, invalid(field)
		}
		set.add(u)
	}
	if set.seen == nil {
		set.seen = map[[16]byte]bool{}
	}
	return set, nil
}

// dateBounds collects the date filters of one column.
type dateBounds struct {
	from, to   *time.Time
	notFrom    []time.Time
	notTo      []time.Time
	contradict bool
}

func later(a *time.Time, b time.Time) *time.Time {
	if a == nil || b.After(*a) {
		return &b
	}
	return a
}

func earlier(a *time.Time, b time.Time) *time.Time {
	if a == nil || b.Before(*a) {
		return &b
	}
	return a
}

func (b *dateBounds) add(f search.Filter, loc *time.Location) {
	from, to := f.Range(loc)
	switch {
	case !f.Negated:
		if from != nil {
			b.from = later(b.from, *from)
		}
		if to != nil {
			b.to = earlier(b.to, *to)
		}
	case from == nil && to == nil:
		b.contradict = true
	case from == nil: // not before to: at or after to
		b.from = later(b.from, *to)
	case to == nil: // not from on: before from
		b.to = earlier(b.to, *from)
	default:
		b.notFrom = append(b.notFrom, *from)
		b.notTo = append(b.notTo, *to)
	}
	if b.from != nil && b.to != nil && !b.from.Before(*b.to) {
		b.contradict = true
	}
}

// searchFilters resolves q's filters into args. It reports none when no
// document can match (an unknown name in a positive filter, or filters
// that contradict each other), so the search need not run.
func (s *Store) searchFilters(ctx context.Context, sc Scope, q search.Query, args *searchArgs) (none bool, err error) {
	tagIDs, err := parseIDs("tag", q.TagIDs)
	if err != nil {
		return false, err
	}
	corrIDs, err := parseIDs("correspondent", q.CorrespondentIDs)
	if err != nil {
		return false, err
	}
	typeIDs, err := parseIDs("type", q.TypeIDs)
	if err != nil {
		return false, err
	}

	// Look up all names at once: tags with their descendants, then
	// correspondents and types. owner[i] is the filter of name i.
	var tagNames, linkNames, linkKinds []string
	var tagOwner, linkOwner []int
	for i, f := range q.Filters {
		for _, v := range f.Values {
			if !validText(v) {
				return false, invalid("q")
			}
			switch f.Field {
			case search.FieldTag:
				tagNames = append(tagNames, v)
				tagOwner = append(tagOwner, i)
			case search.FieldCorrespondent, search.FieldType:
				kind := "c"
				if f.Field == search.FieldType {
					kind = "d"
				}
				linkNames = append(linkNames, v)
				linkKinds = append(linkKinds, kind)
				linkOwner = append(linkOwner, i)
			}
		}
	}
	matched := make([]idSet, len(q.Filters)) // the IDs each name filter stands for
	var idTags idSet                         // the tags of q.TagIDs with their descendants
	if len(tagNames) > 0 || len(q.TagIDs) > 0 {
		rows, err := s.sq.SearchTagIDs(ctx, db.SearchTagIDsParams{Names: tagNames, Owner: sc.owner(), Ids: tagIDs.list()})
		if err != nil {
			return false, err
		}
		for _, r := range rows {
			if r.Ord == 0 {
				idTags.add(r.ID)
			} else {
				matched[tagOwner[r.Ord-1]].add(r.ID)
			}
		}
	}
	if len(linkNames) > 0 {
		rows, err := s.sq.SearchLinkIDs(ctx, db.SearchLinkIDsParams{Names: linkNames, Kinds: linkKinds, Owner: sc.owner()})
		if err != nil {
			return false, err
		}
		for _, r := range rows {
			matched[linkOwner[r.Ord-1]].add(r.ID)
		}
	}

	var notTags, notCorr, notTypes idSet
	var corr, types *idSet
	var asns *map[int32]bool
	var notASNs []int32
	var dates, added dateBounds
	group := int32(0)
	addTagGroup := func(set idSet) {
		for _, id := range set.ids {
			args.TagIds = append(args.TagIds, id)
			args.TagGroups = append(args.TagGroups, group)
		}
		group++
	}
	if len(q.TagIDs) > 0 {
		if len(idTags.ids) == 0 {
			return true, nil
		}
		addTagGroup(idTags)
	}
	if len(q.CorrespondentIDs) > 0 {
		corr = intersect(corr, corrIDs)
	}
	if len(q.TypeIDs) > 0 {
		types = intersect(types, typeIDs)
	}
	for i, f := range q.Filters {
		set := matched[i]
		if set.seen == nil {
			set.seen = map[[16]byte]bool{}
		}
		switch f.Field {
		case search.FieldTag:
			if f.Negated {
				notTags.add(set.ids...)
				continue
			}
			if len(set.ids) == 0 {
				return true, nil
			}
			addTagGroup(set)
		case search.FieldCorrespondent:
			if f.Negated {
				notCorr.add(set.ids...)
			} else {
				corr = intersect(corr, set)
			}
		case search.FieldType:
			if f.Negated {
				notTypes.add(set.ids...)
			} else {
				types = intersect(types, set)
			}
		case search.FieldASN:
			vals := map[int32]bool{}
			for _, v := range f.Values {
				n, err := strconv.ParseInt(v, 10, 32)
				if err != nil {
					return false, invalid("q")
				}
				vals[int32(n)] = true
			}
			switch {
			case f.Negated:
				for n := range vals {
					notASNs = append(notASNs, n)
				}
			case asns == nil:
				asns = &vals
			default:
				for n := range *asns {
					if !vals[n] {
						delete(*asns, n)
					}
				}
			}
		case search.FieldDate:
			dates.add(f, q.Location)
		case search.FieldAdded:
			added.add(f, q.Location)
		case search.FieldIs:
			want := !f.Negated
			flag := &args.Inbox
			if f.Values[0] == search.IsFavorite {
				flag = &args.Favorite
			}
			if *flag != nil && **flag != want {
				return true, nil
			}
			*flag = &want
		}
	}
	if corr != nil {
		if len(corr.ids) == 0 {
			return true, nil
		}
		args.Correspondents = corr.list()
	}
	if types != nil {
		if len(types.ids) == 0 {
			return true, nil
		}
		args.Types = types.list()
	}
	if asns != nil {
		if len(*asns) == 0 {
			return true, nil
		}
		args.Asns = []int32{}
		for n := range *asns {
			args.Asns = append(args.Asns, n)
		}
	}
	if dates.contradict || added.contradict {
		return true, nil
	}
	args.TagGroupCount = group
	args.NotTagIds, args.NotCorrespondents, args.NotTypes = notTags.list(), notCorr.list(), notTypes.list()
	args.NotAsns = notASNs
	args.DateFrom, args.DateTo, args.NotDateFrom, args.NotDateTo = dates.from, dates.to, dates.notFrom, dates.notTo
	args.AddedFrom, args.AddedTo, args.NotAddedFrom, args.NotAddedTo = added.from, added.to, added.notFrom, added.notTo
	return false, nil
}
