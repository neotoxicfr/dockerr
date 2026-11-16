package config

import "testing"

func TestParseClock(t *testing.T) {
	for _, c := range []struct {
		in   string
		h, m int
		ok   bool
	}{
		{"04:00", 4, 0, true}, {"23:59", 23, 59, true}, {"00:00", 0, 0, true}, {" 04:30 ", 4, 30, true},
		{"4:00", 0, 0, false}, {"24:00", 0, 0, false}, {"12:60", 0, 0, false}, {"12", 0, 0, false},
		{"ab:cd", 0, 0, false}, {"", 0, 0, false}, {"-1:00", 0, 0, false}, {"12:5", 0, 0, false},
	} {
		h, m, err := ParseClock(c.in)
		if (err == nil) != c.ok || (c.ok && (h != c.h || m != c.m)) {
			t.Errorf("%q: got %d:%d, %v", c.in, h, m, err)
		}
	}
}
