package server

import (
	"testing"

	"github.com/neotoxicfr/dockerr/internal/stacks"
)

func TestNetmapNoDuplicate(t *testing.T) {
	c := func(name, project, mode, parent string, nets ...string) *stacks.Container {
		return &stacks.Container{ID: name + "-id", Name: name, Project: project, NetworkMode: mode, NetParent: parent, Networks: nets, State: "running",
			IPs: map[string]string{}}
	}
	all := []*stacks.Container{
		c("traefik", "proxy", "bridge", "", "edge", "local", "auth"),
		c("authelia", "proxy", "bridge", "", "auth", "local"),
		c("gluetun", "torrent", "bridge", "", "local"),
		c("qbit", "torrent", "container:gluetun", "gluetun"),
		c("ghost", "torrent", "container:abcdef123456", "abcdef123456"),
		c("tailscale", "system", "host", "", "host"),
		c("lonely", "", "none", "", "none"),
	}
	m := buildNetMap(all, map[string]mapCol{"auth": {Internal: true}})
	seen := map[string]int{}
	for _, g := range m.Groups {
		for _, r := range g.Rows {
			seen[r.C.Name]++
			for _, ch := range r.Children {
				seen[ch.Name]++
			}
		}
	}
	for _, x := range all {
		if seen[x.Name] != 1 {
			t.Errorf("%s appears %d times", x.Name, seen[x.Name])
		}
	}
	if m.Total != len(all) {
		t.Errorf("total %d, want %d", m.Total, len(all))
	}
	var cols []string
	for _, col := range m.Cols {
		cols = append(cols, col.Name)
	}
	if got := cols; len(got) != 4 || got[0] != "local" || got[3] != "host" {
		t.Fatalf("columns %v: want local first, host last, none dropped", got)
	}
	if !m.Cols[1].Internal && !m.Cols[2].Internal {
		t.Error("driver info not carried")
	}
	if last := m.Groups[len(m.Groups)-1]; last.Stack != "" {
		t.Errorf("containers outside compose should come last, got %q", last.Stack)
	}
	for _, g := range m.Groups {
		for _, r := range g.Rows {
			if r.C.Name == "ghost" && r.Missing == "" {
				t.Error("orphan child without missing parent")
			}
			if r.C.Name == "gluetun" && (len(r.Children) != 1 || r.Children[0].Name != "qbit") {
				t.Errorf("gluetun children %v", r.Children)
			}
		}
	}
}
