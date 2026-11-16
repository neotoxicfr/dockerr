package server

import (
	"testing"

	"github.com/neotoxicfr/dockerr/internal/store"
)

func TestCompileAlert(t *testing.T) {
	cases := []struct {
		pattern, line string
		match, bad    bool
	}{
		{"error", "2026 ERROR: disk full", true, false},
		{"error", "all good", false, false},
		{"/level=(error|fatal)/", "ts=1 level=fatal msg=x", true, false},
		{"/level=(error|fatal)/", "ts=1 level=info", false, false},
		{"/[/", "", false, true},
		{"   ", "", false, true},
		{"/", "a/b", true, false}, // a lone slash is text
	}
	for _, c := range cases {
		m, err := compileAlert(store.LogAlert{Pattern: c.pattern})
		if (err != nil) != c.bad {
			t.Errorf("compileAlert(%q) err = %v", c.pattern, err)
			continue
		}
		if !c.bad && m.match(c.line) != c.match {
			t.Errorf("%q on %q = %v, want %v", c.pattern, c.line, !c.match, c.match)
		}
	}
}
