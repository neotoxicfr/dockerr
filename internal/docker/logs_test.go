package docker

import (
	"strings"
	"testing"
	"time"
)

func TestParseLogLine(t *testing.T) {
	cases := []struct {
		in, msg string
		ts      bool
	}{
		{"2026-11-16T07:00:00.123456789Z hello world\r", "hello world", true},
		{"no timestamp here", "no timestamp here", false},
		{"2026-11-16T07:00:00Z ", "", true},
		{"2026-11-16T07:00:00Z " + strings.Repeat("x", maxLine+10), strings.Repeat("x", maxLine) + " …", true},
	}
	for _, c := range cases {
		l := ParseLogLine(c.in)
		if l.M != c.msg || l.T.IsZero() == c.ts {
			t.Errorf("ParseLogLine(%.40q) = %.40q, %v", c.in, l.M, l.T)
		}
	}
	if l := ParseLogLine("2026-11-16T07:00:00.5Z x"); !l.T.Equal(time.Date(2026, 11, 16, 7, 0, 0, 5e8, time.UTC)) {
		t.Errorf("time = %v", l.T)
	}
}
