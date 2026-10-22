// Package search parses the document search syntax (docs/guide/search.md):
// free text with phrases, exclusion and OR, and the filters tag:,
// correspondent:, type:, asn:, date: (created:), added: and is:. The
// parser is hand-written and runs in linear time; it never builds SQL.
// store.SearchDocuments turns a Query into bound parameters.
package search

import (
	"errors"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // time zones of the tz parameter, independent of the host's zone files
	"unicode"
	"unicode/utf8"
)

// Limits of a query.
const (
	// MaxBytes is the longest query.
	MaxBytes = 1000
	// MaxItems is the most items in a query: each free-text item and each
	// value of a filter counts.
	MaxItems = 32
	// MaxLimit is the largest page; DefaultLimit is used when Limit is 0.
	MaxLimit     = 100
	DefaultLimit = 100
)

var (
	// ErrTooLong means the query has more than MaxBytes bytes or more than
	// MaxItems items.
	ErrTooLong = errors.New("search: query too long")
	// ErrInvalid means the query is not valid UTF-8 or holds a NUL byte.
	ErrInvalid = errors.New("search: invalid query")
)

// Text is one free-text item: a word, or a phrase in quotes.
type Text struct {
	Value   string
	Phrase  bool
	Negated bool
	// Group joins items by OR: items with the same Group match when any
	// of them does. Groups are joined by AND. Parse numbers them from 0 in
	// order, and the items of one group are adjacent.
	Group int
}

// Field is the name of a filter.
type Field string

// Filter fields. created: is parsed as FieldDate.
const (
	FieldTag           Field = "tag"
	FieldCorrespondent Field = "correspondent"
	FieldType          Field = "type"
	FieldASN           Field = "asn"
	FieldDate          Field = "date"  // the document's date (issue date)
	FieldAdded         Field = "added" // when the document was added
	FieldIs            Field = "is"
)

// Status values of FieldIs.
const (
	IsInbox    = "inbox"
	IsFavorite = "favorite"
)

// Date is a calendar day, independent of a time zone.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// Start is the first instant of the day in loc.
func (d Date) Start(loc *time.Location) time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, loc)
}

func (d Date) String() string {
	return pad(d.Year, 4) + "-" + pad(int(d.Month), 2) + "-" + pad(d.Day, 2)
}

// addDays returns the day n days later (calendar arithmetic, no zone).
func (d Date) addDays(n int) Date {
	t := time.Date(d.Year, d.Month, d.Day+n, 0, 0, 0, 0, time.UTC)
	return Date{t.Year(), t.Month(), t.Day()}
}

func pad(n, width int) string {
	s := strconv.Itoa(n)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

// Filter is one filter item. A comma list in Values means any of them;
// separate filters are joined by AND, and Negated excludes the matches.
type Filter struct {
	Field   Field
	Negated bool
	// Values are names for tag:, correspondent: and type: (matched
	// exactly, ignoring case), numbers for asn:, and one of IsInbox and
	// IsFavorite for is:.
	Values []string
	// From and To bound date: and added: as days [From, To): To is the
	// first day not included. nil leaves that end open.
	From, To *Date
}

// Query is a parsed search plus the options the API sets.
type Query struct {
	Text    []Text
	Filters []Filter
	// Location is the time zone of the date filters' days; nil is UTC.
	Location *time.Location

	// TagIDs, CorrespondentIDs and TypeIDs (UUIDs) each match a document
	// linked to any of them (a tag also through its descendants); they are
	// joined with the rest by AND. They are not part of the syntax.
	TagIDs, CorrespondentIDs, TypeIDs []string
	// Sort is the order; "" is SortRelevance with free text, else SortAdded.
	Sort Sort
	// Ascending reverses SortAdded and SortDate (oldest first).
	Ascending bool
	// Cursor continues after the page that returned it.
	Cursor string
	// Limit is the page size, 1 to MaxLimit; 0 means DefaultLimit.
	Limit int
}

// Sort is the order of results.
type Sort string

// Sort orders. Relevance needs free text; added and date are newest first
// unless Ascending.
const (
	SortRelevance Sort = "relevance"
	SortAdded     Sort = "added"
	SortDate      Sort = "date"
)

// ParseSort parses the sort parameter; "" is the default.
func ParseSort(s string) (Sort, bool) {
	switch Sort(s) {
	case "", SortRelevance, SortAdded, SortDate:
		return Sort(s), true
	}
	return "", false
}

// Range returns the instants [from, to) of a date filter in loc (nil is
// UTC); nil leaves an end open.
func (f Filter) Range(loc *time.Location) (from, to *time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	if f.From != nil {
		t := f.From.Start(loc)
		from = &t
	}
	if f.To != nil {
		t := f.To.Start(loc)
		to = &t
	}
	return from, to
}

// Parse parses a query; loc is the time zone of its dates (nil is UTC).
// Invalid UTF-8 or a NUL byte gives ErrInvalid; more than MaxBytes bytes
// or MaxItems items ErrTooLong. Anything else parses: a filter with an
// unknown name or a value that does not parse is free text.
func Parse(s string, loc *time.Location) (Query, error) {
	if len(s) > MaxBytes {
		return Query{}, ErrTooLong
	}
	if !utf8.ValidString(s) || strings.IndexByte(s, 0) >= 0 {
		return Query{}, ErrInvalid
	}
	q := parse(s)
	if q.items() > MaxItems {
		return Query{}, ErrTooLong
	}
	q.Location = loc
	return q, nil
}

// items counts the query's items against MaxItems.
func (q *Query) items() int {
	n := len(q.Text)
	for _, f := range q.Filters {
		n += max(1, len(f.Values))
	}
	return n
}

// CheckLimits applies Parse's limits to a query built some other way (an
// API that adds filters, a saved view): more than MaxItems items, or text
// and filter values of more than MaxBytes bytes together, give ErrTooLong.
// The values of a parsed query are parts of its input, so a query Parse
// returned passes.
func (q *Query) CheckLimits() error {
	if q.items() > MaxItems {
		return ErrTooLong
	}
	n := 0
	for _, t := range q.Text {
		n += len(t.Value)
	}
	for _, f := range q.Filters {
		for _, v := range f.Values {
			n += len(v)
		}
	}
	if n > MaxBytes {
		return ErrTooLong
	}
	return nil
}

// HasText reports whether the query has free-text items.
func (q *Query) HasText() bool { return len(q.Text) > 0 }

// token is one lexed item before OR is resolved.
type token struct {
	text   *Text
	filter *Filter
	bareOR bool // the word OR, not negated and not quoted
}

// parse parses s without the limits.
func parse(s string) Query {
	var toks []token
	for i := 0; ; {
		i = skipSpace(s, i)
		if i >= len(s) {
			break
		}
		var tok token
		tok, i = lexItem(s, i)
		if tok.text != nil || tok.filter != nil {
			toks = append(toks, tok)
		}
	}

	var q Query
	group := -1
	joinNext := false
	for i, tok := range toks {
		if tok.filter != nil {
			q.Filters = append(q.Filters, *tok.filter)
			continue
		}
		if tok.bareOR {
			// OR joins two free-text items; anywhere else it is a word.
			prevText := i > 0 && toks[i-1].text != nil
			nextText := i+1 < len(toks) && toks[i+1].text != nil && !toks[i+1].bareOR
			if prevText && nextText {
				joinNext = true
				continue
			}
			// A literal "or" searches the same as "OR"; lowercase keeps
			// String from making it an operator.
			tok.text.Value = "or"
		}
		if !joinNext {
			group++
		}
		joinNext = false
		t := *tok.text
		t.Group = group
		q.Text = append(q.Text, t)
	}
	return q
}

func isSpace(r rune) bool { return unicode.IsSpace(r) }

func skipSpace(s string, i int) int {
	for i < len(s) {
		r, n := utf8.DecodeRuneInString(s[i:])
		if !isSpace(r) {
			break
		}
		i += n
	}
	return i
}

// wordEnd returns the end of the run of non-space characters at i.
func wordEnd(s string, i int) int {
	for i < len(s) {
		r, n := utf8.DecodeRuneInString(s[i:])
		if isSpace(r) {
			break
		}
		i += n
	}
	return i
}

// lexItem reads the item at i (not a space) and returns the position after
// it. An item yields nothing when it has no content (a lone "-", "").
func lexItem(s string, i int) (token, int) {
	neg := false
	if s[i] == '-' && i+1 < len(s) {
		if r, _ := utf8.DecodeRuneInString(s[i+1:]); !isSpace(r) {
			neg = true
			i++
		}
	}
	if s[i] == '"' {
		// A phrase runs to the closing quote, or to the end.
		end := strings.IndexByte(s[i+1:], '"')
		var body string
		next := len(s)
		if end >= 0 {
			body = s[i+1 : i+1+end]
			next = i + 1 + end + 1
		} else {
			body = s[i+1:]
		}
		body = strings.TrimSpace(body)
		if body == "" {
			return token{}, next
		}
		return token{text: &Text{Value: body, Phrase: true, Negated: neg}}, next
	}
	f, reached, ok := lexFilter(s, i)
	if ok {
		f.Negated = neg
		return token{filter: f}, reached
	}
	// The word includes what the failed filter read, which may be a
	// quoted value with spaces: read again, it fails the same way.
	end := wordEnd(s, max(i, reached))
	w := s[i:end]
	if w == "-" && !neg {
		return token{}, end
	}
	return token{text: &Text{Value: w, Negated: neg}, bareOR: !neg && w == "OR"}, end
}

// lexFilter reads name:values at i and returns the position after them.
// It fails, so the item is a word, when the name is unknown, a value is
// malformed or something other than a space follows the values; it then
// returns how far it read.
func lexFilter(s string, i int) (*Filter, int, bool) {
	colon := -1
	for j := i; j < len(s) && j-i <= len("correspondent"); j++ {
		c := s[j]
		if c == ':' {
			colon = j
			break
		}
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return nil, i, false
		}
	}
	if colon < 0 {
		return nil, i, false
	}
	var field Field
	switch strings.ToLower(s[i:colon]) {
	case "tag":
		field = FieldTag
	case "correspondent":
		field = FieldCorrespondent
	case "type":
		field = FieldType
	case "asn":
		field = FieldASN
	case "date", "created":
		field = FieldDate
	case "added":
		field = FieldAdded
	case "is":
		field = FieldIs
	default:
		return nil, i, false
	}
	values, next, ok := lexValues(s, colon+1)
	if !ok {
		return nil, next, false
	}
	f := &Filter{Field: field}
	switch field {
	case FieldTag, FieldCorrespondent, FieldType:
		f.Values = values
	case FieldASN:
		for _, v := range values {
			if !validASN(v) {
				return nil, next, false
			}
		}
		f.Values = values
	case FieldIs:
		if len(values) != 1 {
			return nil, next, false
		}
		switch v := strings.ToLower(values[0]); v {
		case IsInbox, IsFavorite:
			f.Values = []string{v}
		default:
			return nil, next, false
		}
	case FieldDate, FieldAdded:
		if len(values) != 1 {
			return nil, next, false
		}
		from, to, ok := parseDateRange(values[0])
		if !ok {
			return nil, next, false
		}
		f.From, f.To = from, to
	}
	return f, next, true
}

// lexValues reads a comma list of values at i: each is quoted (to the
// closing quote, or to the end) or a run without spaces, commas and
// quotes. Empty values are dropped; at least one must remain. On failure
// it returns how far it read.
func lexValues(s string, i int) ([]string, int, bool) {
	var values []string
	for {
		var v string
		if i < len(s) && s[i] == '"' {
			end := strings.IndexByte(s[i+1:], '"')
			if end < 0 {
				v, i = s[i+1:], len(s)
			} else {
				v, i = s[i+1:i+1+end], i+1+end+1
			}
		} else {
			j := i
			for j < len(s) {
				r, n := utf8.DecodeRuneInString(s[j:])
				if isSpace(r) || r == ',' || r == '"' {
					break
				}
				j += n
			}
			v, i = s[i:j], j
		}
		if v = strings.TrimSpace(v); v != "" {
			values = append(values, v)
		}
		if i < len(s) && s[i] == ',' {
			i++
			continue
		}
		break
	}
	if i < len(s) {
		if r, _ := utf8.DecodeRuneInString(s[i:]); !isSpace(r) {
			return nil, i, false
		}
	}
	return values, i, len(values) > 0
}

// validASN reports whether v is an archive serial number: 1 to 2^31-1,
// digits only.
func validASN(v string) bool {
	for _, c := range []byte(v) {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.ParseInt(v, 10, 32)
	return err == nil && n > 0
}

// parseDateRange parses the value of date: and added: into days [from,
// to): a year, month or day; a range a..b with either end open; or a
// comparison >, >=, <, <=.
func parseDateRange(v string) (from, to *Date, ok bool) {
	switch {
	case strings.HasPrefix(v, ">="):
		lo, _, ok := parseDateUnit(v[2:])
		return ptr(lo), nil, ok
	case strings.HasPrefix(v, ">"):
		_, hi, ok := parseDateUnit(v[1:])
		return ptr(hi), nil, ok
	case strings.HasPrefix(v, "<="):
		_, hi, ok := parseDateUnit(v[2:])
		return nil, ptr(hi), ok
	case strings.HasPrefix(v, "<"):
		lo, _, ok := parseDateUnit(v[1:])
		return nil, ptr(lo), ok
	}
	if a, b, found := strings.Cut(v, ".."); found {
		if a == "" && b == "" {
			return nil, nil, false
		}
		if a != "" {
			lo, _, ok := parseDateUnit(a)
			if !ok {
				return nil, nil, false
			}
			from = &lo
		}
		if b != "" {
			_, hi, ok := parseDateUnit(b)
			if !ok {
				return nil, nil, false
			}
			to = &hi
		}
		return from, to, true
	}
	lo, hi, ok := parseDateUnit(v)
	return ptr(lo), ptr(hi), ok
}

func ptr(d Date) *Date { return &d }

// parseDateUnit parses YYYY, YYYY-MM or YYYY-MM-DD and returns its first
// day and the first day after it.
func parseDateUnit(v string) (lo, hi Date, ok bool) {
	parts := strings.Split(v, "-")
	if len(parts) > 3 || len(parts[0]) != 4 {
		return Date{}, Date{}, false
	}
	nums := make([]int, len(parts))
	for i, p := range parts {
		if i > 0 && len(p) != 2 {
			return Date{}, Date{}, false
		}
		for _, c := range []byte(p) {
			if c < '0' || c > '9' {
				return Date{}, Date{}, false
			}
		}
		nums[i], _ = strconv.Atoi(p)
	}
	y := nums[0]
	if y < 1 {
		return Date{}, Date{}, false
	}
	switch len(nums) {
	case 1:
		return Date{y, 1, 1}, Date{y + 1, 1, 1}, true
	case 2:
		m := nums[1]
		if m < 1 || m > 12 {
			return Date{}, Date{}, false
		}
		lo = Date{y, time.Month(m), 1}
		if m == 12 {
			return lo, Date{y + 1, 1, 1}, true
		}
		return lo, Date{y, time.Month(m + 1), 1}, true
	default:
		m, d := nums[1], nums[2]
		t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
		if m < 1 || m > 12 || d < 1 || t.Day() != d || int(t.Month()) != m {
			return Date{}, Date{}, false
		}
		lo = Date{y, time.Month(m), d}
		return lo, lo.addDays(1), true
	}
}

// String renders the query's text and filters in the syntax; Parse of the
// result gives the same items. Options (IDs, sort, cursor) are left out.
// Dates are written as days.
func (q Query) String() string {
	// Filters come first: a word that ends in an unclosed quote (tag:")
	// can only be the last item.
	var parts []string
	for _, f := range q.Filters {
		parts = append(parts, f.String())
	}
	for i, t := range q.Text {
		var b strings.Builder
		if i > 0 && q.Text[i-1].Group == t.Group {
			b.WriteString("OR ")
		}
		if t.Negated {
			b.WriteByte('-')
		}
		if t.Phrase {
			b.WriteString(`"` + t.Value + `"`)
		} else {
			b.WriteString(t.Value)
		}
		parts = append(parts, b.String())
	}
	return strings.Join(parts, " ")
}

func (f Filter) String() string {
	var b strings.Builder
	if f.Negated {
		b.WriteByte('-')
	}
	b.WriteString(string(f.Field) + ":")
	switch f.Field {
	case FieldDate, FieldAdded:
		switch {
		case f.From != nil && f.To != nil && f.From.addDays(1) == *f.To:
			b.WriteString(f.From.String())
		case f.From != nil && f.To != nil:
			b.WriteString(f.From.String() + ".." + f.To.addDays(-1).String())
		// One end: the day itself, unless it lies outside the years 1 to
		// 9999 the syntax has (">9999" gives From 10000-01-01).
		case f.From != nil && f.From.Year <= 9999:
			b.WriteString(">=" + f.From.String())
		case f.From != nil:
			b.WriteString(">" + f.From.addDays(-1).String())
		case f.To != nil && f.To.Year <= 9999:
			b.WriteString("<" + f.To.String())
		case f.To != nil:
			b.WriteString("<=" + f.To.addDays(-1).String())
		}
	default:
		for i, v := range f.Values {
			if i > 0 {
				b.WriteByte(',')
			}
			if strings.IndexFunc(v, func(r rune) bool { return isSpace(r) || r == ',' }) >= 0 {
				b.WriteString(`"` + v + `"`)
			} else {
				b.WriteString(v)
			}
		}
	}
	return b.String()
}
