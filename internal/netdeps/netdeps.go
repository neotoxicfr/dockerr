// Package netdeps tracks containers that join another container's network
// namespace (network_mode: container:X or service:X) and finds the ones left
// without network after their parent was restarted or recreated.
//
// Docker resolves the parent once, when the child starts. If the parent then
// restarts, it gets a new namespace and the child keeps the dead one. If the
// parent is recreated, its ID changes and the child's reference dangles.
package netdeps

import (
	"context"
	"strings"
	"time"

	"github.com/neotoxicfr/dockerr/internal/stacks"
)

// Action repairs a child: a restart rejoins the parent when the reference
// still resolves (same ID, or a name); a recreate is needed when the
// reference is the ID of a container that no longer exists.
type Action string

const (
	Restart  Action = "restart"
	Recreate Action = "recreate"
)

type Fix struct {
	Child  *stacks.Container
	Parent *stacks.Container // nil when gone
	Action Action
}

// StartedFunc returns when a container last started.
type StartedFunc func(ctx context.Context, id string) (time.Time, error)

// Ref returns the parent reference of a container:X network mode, or "".
func Ref(mode string) string {
	ref, ok := strings.CutPrefix(mode, "container:")
	if !ok {
		return ""
	}
	return ref
}

// resolve finds the container a reference points to: full ID, ID prefix of
// 12+ characters, or name.
func resolve(ref string, all []*stacks.Container) *stacks.Container {
	for _, c := range all {
		if c.ID == ref || c.Name == ref || (len(ref) >= 12 && strings.HasPrefix(c.ID, ref)) {
			return c
		}
	}
	return nil
}

func isID(ref string) bool {
	if len(ref) < 12 {
		return false
	}
	for _, r := range ref {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Analyze sets NetParent and NetLost on every container of all, and returns
// the fixes for the lost ones.
func Analyze(ctx context.Context, all []*stacks.Container, started StartedFunc) []Fix {
	var fixes []Fix
	for _, c := range all {
		ref := Ref(c.NetworkMode)
		if ref == "" {
			continue
		}
		p := resolve(ref, all)
		if p != nil {
			c.NetParent = p.Name
		} else {
			c.NetParent = ref
			if isID(ref) && len(ref) > 12 {
				c.NetParent = ref[:12]
			}
		}
		if !c.Running() {
			continue
		}
		lost := p == nil || !p.Running()
		if !lost && started != nil {
			ps, err1 := started(ctx, p.ID)
			cs, err2 := started(ctx, c.ID)
			lost = err1 == nil && err2 == nil && ps.After(cs)
		}
		if !lost {
			continue
		}
		c.NetLost = true
		f := Fix{Child: c, Parent: p, Action: Restart}
		if p == nil && isID(ref) {
			f.Action = Recreate
		}
		fixes = append(fixes, f)
	}
	return fixes
}

// Children lists the containers joining parent's namespace.
func Children(parent *stacks.Container, all []*stacks.Container) []*stacks.Container {
	var out []*stacks.Container
	for _, c := range all {
		if ref := Ref(c.NetworkMode); ref != "" && resolve(ref, all) == parent {
			out = append(out, c)
		}
	}
	return out
}
