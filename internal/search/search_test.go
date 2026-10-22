package search

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func w(v string, group int) Text           { return Text{Value: v, Group: group} }
func nw(v string, group int) Text          { return Text{Value: v, Negated: true, Group: group} }
func ph(v string, group int) Text          { return Text{Value: v, Phrase: true, Group: group} }
func d(y int, m time.Month, day int) *Date { return &Date{y, m, day} }
func fv(f Field, vs ...string) Filter      { return Filter{Field: f, Values: vs} }
func nfv(f Field, vs ...string) Filter     { return Filter{Field: f, Values: vs, Negated: true} }
func fd(f Field, from, to *Date) Filter    { return Filter{Field: f, From: from, To: to} }
func texts(ts ...Text) []Text              { return ts }
func filters(fs ...Filter) []Filter        { return fs }
func mustParse(t *testing.T, s string) Query {
	t.Helper()
	q, err := Parse(s, nil)
	must(t, err)
	return q
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestParse(t *testing.T) {
	for _, c := range []struct {
		in      string
		text    []Text
		filters []Filter
	}{
		{"", nil, nil},
		{"   \t\n ", nil, nil},
		{"Rechnung", texts(w("Rechnung", 0)), nil},
		{"Telekom  Rechnung", texts(w("Telekom", 0), w("Rechnung", 1)), nil},
		{`"zwei Wörter"`, texts(ph("zwei Wörter", 0)), nil},
		{`"  padded  "`, texts(ph("padded", 0)), nil},
		{`"unclosed phrase`, texts(ph("unclosed phrase", 0)), nil},
		{`""`, nil, nil},
		{`a "" b`, texts(w("a", 0), w("b", 1)), nil},
		{"-Mahnung", texts(nw("Mahnung", 0)), nil},
		{`-"zwei Wörter"`, texts(Text{Value: "zwei Wörter", Phrase: true, Negated: true}), nil},
		{"- lone", texts(w("lone", 0)), nil},
		{"trailing -", texts(w("trailing", 0)), nil},
		{"--x", texts(nw("-x", 0)), nil},
		{"a-b", texts(w("a-b", 0)), nil},
		{"a OR b", texts(w("a", 0), w("b", 0)), nil},
		{"a OR b OR c d", texts(w("a", 0), w("b", 0), w("c", 0), w("d", 1)), nil},
		{`a OR "b c"`, texts(w("a", 0), ph("b c", 0)), nil},
		{"a OR -b", texts(w("a", 0), nw("b", 0)), nil},
		{"OR a", texts(w("or", 0), w("a", 1)), nil},
		{"a OR", texts(w("a", 0), w("or", 1)), nil},
		{"a OR OR b", texts(w("a", 0), w("or", 1), w("b", 1)), nil},
		{"a or b", texts(w("a", 0), w("or", 1), w("b", 2)), nil},
		{"a OR tag:x", texts(w("a", 0), w("or", 1)), filters(fv(FieldTag, "x"))},
		{"tag:x OR a", texts(w("or", 0), w("a", 1)), filters(fv(FieldTag, "x"))},
		{"-OR", texts(nw("OR", 0)), nil},
		{"tag:Steuer", nil, filters(fv(FieldTag, "Steuer"))},
		{"TAG:Steuer", nil, filters(fv(FieldTag, "Steuer"))},
		{"tag:Steuer,Auto", nil, filters(fv(FieldTag, "Steuer", "Auto"))},
		{"tag:Steuer,,Auto,", nil, filters(fv(FieldTag, "Steuer", "Auto"))},
		{`tag:"Auto Versicherung"`, nil, filters(fv(FieldTag, "Auto Versicherung"))},
		{`tag:"Auto Versicherung",Steuer`, nil, filters(fv(FieldTag, "Auto Versicherung", "Steuer"))},
		{`tag:"unclosed value`, nil, filters(fv(FieldTag, "unclosed value"))},
		{`tag:"a"b`, texts(w(`tag:"a"b`, 0)), nil},
		{`tag:a"b`, texts(w(`tag:a"b`, 0)), nil},
		{"tag:", texts(w("tag:", 0)), nil},
		// A failed filter is one word with all it read, quotes included.
		{`tag:"0 ""`, texts(w(`tag:"0 ""`, 0)), nil},
		{`tag:0 tag:"`, texts(w(`tag:"`, 0)), filters(fv(FieldTag, "0"))},
		{`tag:""`, texts(w(`tag:""`, 0)), nil},
		{"-tag:Steuer", nil, filters(nfv(FieldTag, "Steuer"))},
		{"correspondent:Telekom", nil, filters(fv(FieldCorrespondent, "Telekom"))},
		{"type:Rechnung,Vertrag", nil, filters(fv(FieldType, "Rechnung", "Vertrag"))},
		{"-type:Rechnung", nil, filters(nfv(FieldType, "Rechnung"))},
		{"asn:123", nil, filters(fv(FieldASN, "123"))},
		{"asn:1,2", nil, filters(fv(FieldASN, "1", "2"))},
		{"asn:0", texts(w("asn:0", 0)), nil},
		{"asn:-1", texts(w("asn:-1", 0)), nil},
		{"asn:12a", texts(w("asn:12a", 0)), nil},
		{"asn:2147483648", texts(w("asn:2147483648", 0)), nil},
		{"is:inbox", nil, filters(fv(FieldIs, IsInbox))},
		{"is:Favorite", nil, filters(fv(FieldIs, IsFavorite))},
		{"-is:inbox", nil, filters(nfv(FieldIs, IsInbox))},
		{"is:inbox,favorite", texts(w("is:inbox,favorite", 0)), nil},
		{"is:read", texts(w("is:read", 0)), nil},
		{"12:30", texts(w("12:30", 0)), nil},
		{"https://example.com/a?b=c", texts(w("https://example.com/a?b=c", 0)), nil},
		{"foo:bar", texts(w("foo:bar", 0)), nil},
		{"Re:", texts(w("Re:", 0)), nil},
		// Dates: years, months, days, ranges and comparisons.
		{"date:2024", nil, filters(fd(FieldDate, d(2024, 1, 1), d(2025, 1, 1)))},
		{"created:2024", nil, filters(fd(FieldDate, d(2024, 1, 1), d(2025, 1, 1)))},
		{"date:2024-03", nil, filters(fd(FieldDate, d(2024, 3, 1), d(2024, 4, 1)))},
		{"date:2024-12", nil, filters(fd(FieldDate, d(2024, 12, 1), d(2025, 1, 1)))},
		{"date:2024-02-29", nil, filters(fd(FieldDate, d(2024, 2, 29), d(2024, 3, 1)))},
		{"date:2024-12-31", nil, filters(fd(FieldDate, d(2024, 12, 31), d(2025, 1, 1)))},
		{"date:2024-01-01..2024-06-30", nil, filters(fd(FieldDate, d(2024, 1, 1), d(2024, 7, 1)))},
		{"date:2024..2025-03", nil, filters(fd(FieldDate, d(2024, 1, 1), d(2025, 4, 1)))},
		{"date:..2024", nil, filters(fd(FieldDate, nil, d(2025, 1, 1)))},
		{"date:2024..", nil, filters(fd(FieldDate, d(2024, 1, 1), nil))},
		{"date:>2026-01-01", nil, filters(fd(FieldDate, d(2026, 1, 2), nil))},
		{"date:>=2026-01-01", nil, filters(fd(FieldDate, d(2026, 1, 1), nil))},
		{"date:<2026-01-01", nil, filters(fd(FieldDate, nil, d(2026, 1, 1)))},
		{"date:<=2026-01-01", nil, filters(fd(FieldDate, nil, d(2026, 1, 2)))},
		{"date:>2024", nil, filters(fd(FieldDate, d(2025, 1, 1), nil))},
		{"date:<=2024-02", nil, filters(fd(FieldDate, nil, d(2024, 3, 1)))},
		{"added:2026-10", nil, filters(fd(FieldAdded, d(2026, 10, 1), d(2026, 11, 1)))},
		{"-added:2026", nil, filters(Filter{Field: FieldAdded, Negated: true, From: d(2026, 1, 1), To: d(2027, 1, 1)})},
		// Invalid dates are text.
		{"date:2023-02-29", texts(w("date:2023-02-29", 0)), nil},
		{"date:2024-13", texts(w("date:2024-13", 0)), nil},
		{"date:2024-00", texts(w("date:2024-00", 0)), nil},
		{"date:2024-1-5", texts(w("date:2024-1-5", 0)), nil},
		{"date:24", texts(w("date:24", 0)), nil},
		{"date:0000", texts(w("date:0000", 0)), nil},
		{"date:..", texts(w("date:..", 0)), nil},
		{"date:2024,2025", texts(w("date:2024,2025", 0)), nil},
		{"date:>>2024", texts(w("date:>>2024", 0)), nil},
		{"date:2024...2025", texts(w("date:2024...2025", 0)), nil},
		{"date:yesterday", texts(w("date:yesterday", 0)), nil},
		// Everything together.
		{`Telekom -Mahnung tag:Steuer correspondent:"Deutsche Telekom" date:2024 is:inbox`,
			texts(w("Telekom", 0), nw("Mahnung", 1)),
			filters(fv(FieldTag, "Steuer"), fv(FieldCorrespondent, "Deutsche Telekom"),
				fd(FieldDate, d(2024, 1, 1), d(2025, 1, 1)), fv(FieldIs, IsInbox))},
	} {
		t.Run(c.in, func(t *testing.T) {
			q, err := Parse(c.in, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(q.Text, c.text) || !reflect.DeepEqual(q.Filters, c.filters) {
				t.Fatalf("Parse(%q):\n text    %+v\n filters %+v\nwant\n text    %+v\n filters %+v", c.in, q.Text, q.Filters, c.text, c.filters)
			}
			again, err := Parse(q.String(), nil)
			if err != nil || !reflect.DeepEqual(again.Text, q.Text) || !reflect.DeepEqual(again.Filters, q.Filters) {
				t.Fatalf("String %q parses to %+v %+v, %v", q.String(), again.Text, again.Filters, err)
			}
		})
	}
}

func TestParseLimits(t *testing.T) {
	if _, err := Parse(strings.Repeat("a", MaxBytes), nil); err != nil {
		t.Errorf("%d bytes: %v", MaxBytes, err)
	}
	if _, err := Parse(strings.Repeat("a", MaxBytes+1), nil); !errors.Is(err, ErrTooLong) {
		t.Errorf("%d bytes: %v", MaxBytes+1, err)
	}
	if _, err := Parse(strings.Repeat("a ", MaxItems), nil); err != nil {
		t.Errorf("%d items: %v", MaxItems, err)
	}
	if _, err := Parse(strings.Repeat("a ", MaxItems+1), nil); !errors.Is(err, ErrTooLong) {
		t.Errorf("%d items: %v", MaxItems+1, err)
	}
	// Each value of a comma list counts.
	if _, err := Parse("tag:"+strings.Repeat("a,", MaxItems)+"a", nil); !errors.Is(err, ErrTooLong) {
		t.Errorf("%d tag values: %v", MaxItems+1, err)
	}
	if _, err := Parse(strings.Repeat("a ", MaxItems-1)+"date:2024", nil); err != nil {
		t.Errorf("%d items with a date: %v", MaxItems, err)
	}
	for _, bad := range []string{"a\x00b", "\xff", "ok \xc3"} {
		if _, err := Parse(bad, nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// CheckLimits holds a query built by hand to Parse's limits.
func TestCheckLimits(t *testing.T) {
	words := func(n int) []Text {
		out := make([]Text, n)
		for i := range out {
			out[i] = Text{Value: "a", Group: i}
		}
		return out
	}
	inbox := Filter{Field: FieldIs, Values: []string{IsInbox}}
	for _, c := range []struct {
		name string
		q    Query
		ok   bool
	}{
		{"empty", Query{}, true},
		{"MaxItems words", Query{Text: words(MaxItems)}, true},
		{"one item more", Query{Text: words(MaxItems), Filters: []Filter{inbox}}, false},
		{"a filter's values count", Query{Text: words(MaxItems - 1), Filters: []Filter{{Field: FieldTag, Values: []string{"x", "y"}}}}, false},
		{"MaxBytes", Query{Text: []Text{{Value: strings.Repeat("a", MaxBytes-1)}}, Filters: []Filter{{Field: FieldTag, Values: []string{"x"}}}}, true},
		{"one byte more", Query{Text: []Text{{Value: strings.Repeat("a", MaxBytes)}}, Filters: []Filter{{Field: FieldTag, Values: []string{"x"}}}}, false},
	} {
		err := c.q.CheckLimits()
		if c.ok != (err == nil) || err != nil && !errors.Is(err, ErrTooLong) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// Day boundaries are those of the given time zone, also on a day with a
// DST change; nil is UTC.
func TestDateRangeTimeZones(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	must(t, err)
	q, err := Parse("date:2026-03-29", berlin) // clocks go forward at 02:00
	must(t, err)
	from, to := q.Filters[0].Range(q.Location)
	if want := time.Date(2026, 3, 28, 23, 0, 0, 0, time.UTC); !from.Equal(want) {
		t.Errorf("from %v, want %v", from.UTC(), want)
	}
	if want := time.Date(2026, 3, 29, 22, 0, 0, 0, time.UTC); !to.Equal(want) {
		t.Errorf("to %v, want %v (a day of 23 hours)", to.UTC(), want)
	}
	q, err = Parse("added:2026-10-25", berlin) // clocks go back at 03:00
	must(t, err)
	from, to = q.Filters[0].Range(q.Location)
	if to.Sub(*from) != 25*time.Hour {
		t.Errorf("a day of %v, want 25h", to.Sub(*from))
	}
	q = mustParse(t, "date:2026-03-29")
	from, to = q.Filters[0].Range(q.Location)
	if !from.Equal(time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC)) || to.Sub(*from) != 24*time.Hour {
		t.Errorf("UTC: %v..%v", from, to)
	}
	q = mustParse(t, "date:..2024")
	if from, to := q.Filters[0].Range(nil); from != nil || !to.Equal(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("open start: %v..%v", from, to)
	}
}

func TestParseSort(t *testing.T) {
	for _, ok := range []string{"", "relevance", "added", "date"} {
		if _, valid := ParseSort(ok); !valid {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"title", "Relevance", " added"} {
		if _, valid := ParseSort(bad); valid {
			t.Errorf("%q accepted", bad)
		}
	}
}

// Rendering the extreme dates keeps them within the years 1 to 9999.
func TestStringExtremeDates(t *testing.T) {
	for _, in := range []string{"date:>9999", "date:<0001", "date:9999", "date:0001", "date:<=9999-12-31", "date:>=0001-01-01"} {
		q := mustParse(t, in)
		again, err := Parse(q.String(), nil)
		if err != nil || !reflect.DeepEqual(again.Filters, q.Filters) || len(again.Text) != 0 {
			t.Errorf("%q → %q → %+v %+v %v", in, q.String(), again.Filters, again.Text, err)
		}
	}
}

// FuzzParse: Parse never panics, its result is within the limits, and
// parsing its String gives the same items.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"", "Rechnung", `"zwei Wörter"`, `-"x y`, "a OR b OR -c", "OR OR OR", "a OR OR b",
		"tag:Steuer,Auto -tag:x", `tag:"Auto Versicherung",Steuer`, `tag:"a"b`, "asn:123,7",
		"date:2024", "date:2024-03..2024-06-15", "date:>=2026-01-01", "added:<2026", "created:..2024",
		"date:>9999", "date:<0001", "is:inbox -is:favorite", "12:30", "https://x.y/z", "--x", "- -",
		`'; DROP TABLE documents; --`, `&|!():*<->`, "%_\\", "Straße Gebühren Café", " a b",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		q, err := Parse(s, nil)
		if err != nil {
			if !errors.Is(err, ErrTooLong) && !errors.Is(err, ErrInvalid) {
				t.Fatalf("unexpected error %v", err)
			}
			if errors.Is(err, ErrInvalid) && utf8.ValidString(s) && !strings.ContainsRune(s, 0) {
				t.Fatalf("valid text refused: %q", s)
			}
			return
		}
		if len(s) > MaxBytes || q.items() > MaxItems {
			t.Fatalf("over the limits: %d bytes, %d items", len(s), q.items())
		}
		if err := q.CheckLimits(); err != nil {
			t.Fatalf("a parsed query fails CheckLimits: %v", err)
		}
		for i, x := range q.Text {
			if x.Value == "" || !utf8.ValidString(x.Value) {
				t.Fatalf("text %d: %q", i, x.Value)
			}
			if i > 0 && x.Group != q.Text[i-1].Group && x.Group != q.Text[i-1].Group+1 {
				t.Fatalf("groups not in order: %+v", q.Text)
			}
		}
		for _, f := range q.Filters {
			if f.Field == FieldDate || f.Field == FieldAdded {
				if f.From == nil && f.To == nil {
					t.Fatalf("date filter without bounds: %+v", f)
				}
			} else if len(f.Values) == 0 {
				t.Fatalf("filter without values: %+v", f)
			}
		}
		// The limits do not apply: String may be longer than the input
		// (a closing quote, a day instead of a year).
		again := parse(q.String())
		if !reflect.DeepEqual(again.Text, q.Text) || !reflect.DeepEqual(again.Filters, q.Filters) {
			t.Fatalf("%q → String %q →\n %+v %+v\nwant\n %+v %+v", s, q.String(), again.Text, again.Filters, q.Text, q.Filters)
		}
	})
}
