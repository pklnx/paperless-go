package ingest

import (
	"bytes"
	"errors"
	"io"
	"math"
	"testing"
)

func TestLimitReader(t *testing.T) {
	data := bytes.Repeat([]byte{'x'}, 100)
	for _, tc := range []struct {
		limit int64
		want  int // bytes read before the end
		large bool
	}{
		{100, 100, false},
		{99, 99, true},
		{0, 0, true},
		{math.MaxInt64, 100, false},
		{math.MaxInt64 - 1, 100, false},
	} {
		got, err := io.ReadAll(&limitReader{r: bytes.NewReader(data), n: tc.limit})
		if len(got) != tc.want || errors.Is(err, errTooLarge) != tc.large || (!tc.large && err != nil) {
			t.Errorf("limit %d: read %d, %v; want %d, too large %v", tc.limit, len(got), err, tc.want, tc.large)
		}
	}
}
