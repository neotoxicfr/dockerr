package server

import (
	"slices"
	"testing"
	"time"

	"github.com/neotoxicfr/dockerr/internal/stacks"
)

func TestNextWake(t *testing.T) {
	loc := time.FixedZone("x", 3600)
	at := func(d, h, m int) time.Time { return time.Date(2026, 3, d, h, m, 0, 0, loc) }
	for _, c := range []struct {
		name      string
		now, want time.Time
	}{
		{"before", at(10, 3, 0), at(10, 4, 0)},
		{"after", at(10, 5, 0), at(11, 4, 0)},
		{"exactly now waits a day", at(10, 4, 0), at(11, 4, 0)},
		{"month end", time.Date(2026, 3, 31, 23, 0, 0, 0, loc), time.Date(2026, 4, 1, 4, 0, 0, 0, loc)},
	} {
		if got := nextWake(c.now, 4, 0); !got.Equal(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func svc(name string, update, label bool) *stacks.Service {
	c := &stacks.Container{Labels: map[string]string{}}
	if label {
		c.Labels[autoLabel] = "true"
	}
	return &stacks.Service{Name: name, Update: update, Containers: []*stacks.Container{c}}
}

func TestPickTargets(t *testing.T) {
	list := []*stacks.Stack{
		{Name: "opted", File: "c.yml", Services: []*stacks.Service{svc("a", true, false), svc("b", false, false)}},
		{Name: "optedUpToDate", File: "c.yml", Services: []*stacks.Service{svc("a", false, false)}},
		{Name: "labels", File: "c.yml", Services: []*stacks.Service{svc("a", true, true), svc("b", true, false), svc("c", false, true), svc("d", true, true)}},
		{Name: "none", File: "c.yml", Services: []*stacks.Service{svc("a", true, false)}},
		{Name: "self", File: "c.yml", Self: true, Services: []*stacks.Service{svc("a", true, true)}},
		{Name: "nofile", Services: []*stacks.Service{svc("a", true, true)}},
		// Opt-in of the stack wins over labels: one whole-stack operation.
		{Name: "both", File: "c.yml", Services: []*stacks.Service{svc("a", true, true), svc("b", true, true)}},
	}
	got := pickTargets(list, map[string]bool{"opted": true, "optedUpToDate": true, "self": true, "both": true})
	want := []autoTarget{{"opted", ""}, {"labels", "a"}, {"labels", "d"}, {"both", ""}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
