package server

import (
	"os"
	"testing"

	"github.com/neotoxicfr/dockerr/internal/stacks"
)

func TestIsSelf(t *testing.T) {
	host, _ := os.Hostname()
	if len(host) < 12 {
		t.Skip("host name too short to be a container ID")
	}
	cases := []struct {
		id   string
		want bool
	}{
		{host + "0123456789abcdef", true},
		{"ffffffffffff" + host, false},
		{"", false},
	}
	for _, c := range cases {
		if got := isSelf(&stacks.Container{ID: c.id}); got != c.want {
			t.Errorf("isSelf(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}
