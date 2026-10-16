package netdeps

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/neotoxicfr/dockerr/internal/stacks"
)

const (
	gluetunID = "aaaaaaaaaaaa1111111111111111111111111111111111111111111111111111"
	wgID      = "bbbbbbbbbbbb2222222222222222222222222222222222222222222222222222"
)

func ctr(id, name, state, mode string) *stacks.Container {
	return &stacks.Container{ID: id, Name: name, State: state, NetworkMode: mode}
}

func TestAnalyze(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	starts := map[string]time.Time{}
	started := func(_ context.Context, id string) (time.Time, error) {
		s, ok := starts[id]
		if !ok {
			return time.Time{}, errors.New("unknown")
		}
		return s, nil
	}

	tests := []struct {
		name       string
		all        []*stacks.Container
		starts     map[string]time.Time
		wantLost   map[string]Action
		wantParent map[string]string
	}{
		{
			name: "by name, parent restarted after child",
			all: []*stacks.Container{
				ctr(gluetunID, "gluetun", "running", "bridge"),
				ctr("c1", "qbittorrent", "running", "container:gluetun"),
			},
			starts:     map[string]time.Time{gluetunID: t0.Add(time.Hour), "c1": t0},
			wantLost:   map[string]Action{"qbittorrent": Restart},
			wantParent: map[string]string{"qbittorrent": "gluetun"},
		},
		{
			name: "by name, healthy",
			all: []*stacks.Container{
				ctr(gluetunID, "gluetun", "running", "bridge"),
				ctr("c1", "qbittorrent", "running", "container:gluetun"),
			},
			starts:     map[string]time.Time{gluetunID: t0, "c1": t0.Add(time.Minute)},
			wantLost:   map[string]Action{},
			wantParent: map[string]string{"qbittorrent": "gluetun"},
		},
		{
			name: "by ID (service:), parent recreated",
			all: []*stacks.Container{
				ctr("cccccccccccc3333333333333333333333333333333333333333333333333333", "wake-wg", "running", "bridge"),
				ctr("c2", "guacamole", "running", "container:"+wgID),
			},
			wantLost:   map[string]Action{"guacamole": Recreate},
			wantParent: map[string]string{"guacamole": wgID[:12]},
		},
		{
			name: "parent stopped",
			all: []*stacks.Container{
				ctr(wgID, "wake-wg", "exited", "bridge"),
				ctr("c2", "guacamole", "running", "container:"+wgID),
			},
			wantLost:   map[string]Action{"guacamole": Restart},
			wantParent: map[string]string{"guacamole": "wake-wg"},
		},
		{
			name: "child stopped is not lost",
			all: []*stacks.Container{
				ctr("c2", "guacamole", "exited", "container:"+wgID),
			},
			wantLost:   map[string]Action{},
			wantParent: map[string]string{"guacamole": wgID[:12]},
		},
		{
			name: "host and bridge ignored",
			all: []*stacks.Container{
				ctr("c3", "tailscale", "running", "host"),
				ctr("c4", "app", "running", "bridge"),
			},
			wantLost:   map[string]Action{},
			wantParent: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clear(starts)
			for k, v := range tt.starts {
				starts[k] = v
			}
			fixes := Analyze(context.Background(), tt.all, started)
			got := map[string]Action{}
			for _, f := range fixes {
				got[f.Child.Name] = f.Action
			}
			if len(got) != len(tt.wantLost) {
				t.Fatalf("fixes = %v, want %v", got, tt.wantLost)
			}
			for n, a := range tt.wantLost {
				if got[n] != a {
					t.Errorf("%s: action %q, want %q", n, got[n], a)
				}
			}
			for _, c := range tt.all {
				if want := tt.wantParent[c.Name]; c.NetParent != want {
					t.Errorf("%s: parent %q, want %q", c.Name, c.NetParent, want)
				}
				if _, lost := tt.wantLost[c.Name]; c.NetLost != lost {
					t.Errorf("%s: NetLost %v, want %v", c.Name, c.NetLost, lost)
				}
			}
		})
	}
}

func TestChildren(t *testing.T) {
	g := ctr(gluetunID, "gluetun", "running", "bridge")
	all := []*stacks.Container{g,
		ctr("c1", "qbittorrent", "running", "container:gluetun"),
		ctr("c2", "ratioghost", "running", "container:"+gluetunID),
		ctr("c3", "other", "running", "bridge"),
	}
	if n := len(Children(g, all)); n != 2 {
		t.Fatalf("children = %d, want 2", n)
	}
}
