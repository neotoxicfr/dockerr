package docker

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

func TestScanLongLines(t *testing.T) {
	huge := strings.Repeat("é", 1<<20) // 2 MiB, two bytes per character
	in := "2026-11-16T07:00:00Z " + huge + "\n2026-11-16T07:00:01Z next\nlast without newline"
	out := make(chan LogLine, 8)
	if err := scanLines(t.Context(), strings.NewReader(in), 3, true, out); err != nil {
		t.Fatal(err)
	}
	close(out)
	var got []LogLine
	for l := range out {
		got = append(got, l)
	}
	if len(got) != 3 || got[1].M != "next" || got[2].M != "last without newline" || got[0].C != 3 || !got[0].Err {
		t.Fatalf("lines = %d", len(got))
	}
	if !strings.HasSuffix(got[0].M, " …") || !utf8.ValidString(got[0].M) || len(got[0].M) > maxLine+8 {
		t.Errorf("long line: %d bytes, valid UTF-8 %v", len(got[0].M), utf8.ValidString(got[0].M))
	}
}
