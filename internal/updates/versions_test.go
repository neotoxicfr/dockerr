package updates

import "testing"

func TestNewer(t *testing.T) {
	tags := []string{"1.26", "1.27", "1.27.3", "1.28", "1.29-alpine", "1.29", "1.30rc1", "2.0", "2.1", "2.2-rc1",
		"latest", "alpine", "v1.29", "20251105", "1.28-alpine", "3.0.0"}
	cases := []struct {
		current, minor, major string
	}{
		{"1.27", "1.29", "2.1"},            // same major, then the latest major; 2.2-rc1 has another suffix
		{"1.27.3", "", "3.0.0"},            // three numbers: only 3.0.0 has that shape
		{"1.28-alpine", "1.29-alpine", ""}, // the variant stays the variant
		{"v1.28", "v1.29", ""},             // the prefix too
		{"2.1", "", ""},                    // already the latest
		{"latest", "", ""},                 // not a version
		{"20251104", "", ""},               // a date is a build
		{"1.2rc1", "", ""},                 // not a version shape
	}
	for _, c := range cases {
		minor, major := newer(c.current, tags)
		if minor != c.minor || major != c.major {
			t.Errorf("newer(%q) = %q, %q; want %q, %q", c.current, minor, major, c.minor, c.major)
		}
	}
}
