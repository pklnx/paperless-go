package store

import (
	"strings"
	"testing"
)

func TestNeedsRebuild(t *testing.T) {
	libc := databaseLocale{Provider: "c", Collate: "en_US.utf8", Ctype: "en_US.utf8", ActualVersion: "2.41", ServerMajor: 18}
	// The same database under musl: no collation version.
	musl := libc
	musl.ActualVersion = ""
	builtin := databaseLocale{Provider: "b", Collate: "C.UTF-8", Ctype: "C.UTF-8", Locale: "C.UTF-8", ActualVersion: "1", ServerMajor: 18}
	posix := databaseLocale{Provider: "c", Collate: "C", Ctype: "POSIX", ServerMajor: 18}
	mixed := databaseLocale{Provider: "c", Collate: "C", Ctype: "en_US.utf8", ActualVersion: "2.41", ServerMajor: 18}
	icu := databaseLocale{Provider: "i", Collate: "en_US.utf8", Ctype: "en_US.utf8", Locale: "und", ActualVersion: "153.120", ServerMajor: 18}
	nextMajor := builtin
	nextMajor.ServerMajor = 19

	tests := []struct {
		name   string
		cur    databaseLocale
		stored string
		want   bool
	}{
		{"unknown libc locale", libc, "", true},
		{"unknown ICU locale", icu, "", true},
		{"unknown C and POSIX", posix, "", false},
		{"unknown C collation with a libc ctype", mixed, "", true},
		{"unknown builtin", builtin, "", false},
		{"unchanged libc", libc, libc.fingerprint(), false},
		{"musl to glibc", libc, musl.fingerprint(), true},
		{"glibc update", libc, func() string { l := libc; l.ActualVersion = "2.36"; return l.fingerprint() }(), true},
		{"libc to builtin", builtin, libc.fingerprint(), true},
		{"builtin under a new major", nextMajor, builtin.fingerprint(), true},
		{"unchanged builtin", builtin, builtin.fingerprint(), false},
	}
	for _, tt := range tests {
		if got := needsRebuild(tt.cur, tt.stored); got != tt.want {
			t.Errorf("%s: needsRebuild = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestCheckServerVersion(t *testing.T) {
	for _, tt := range []struct {
		num     int
		version string
		ok      bool
	}{
		{160015, "16.15", false},
		{150010, "15.10 (Debian 15.10-1)", false},
		{170000, "17.0", true},
		{180006, "18.6 (Debian 18.6-1.pgdg13+2)", true},
	} {
		err := checkServerVersion(tt.num, tt.version)
		if tt.ok != (err == nil) {
			t.Errorf("%s: %v", tt.version, err)
		}
		if err != nil && err.Error() != "PostgreSQL 17 or newer is required, found "+tt.version {
			t.Errorf("%s: message %q", tt.version, err)
		}
	}
	if !strings.Contains(checkServerVersion(160015, "16.15").Error(), "17 or newer") {
		t.Error("no minimum in the message")
	}
}
