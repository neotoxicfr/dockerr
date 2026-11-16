// Package updates tells which images have a newer digest on their registry
// than the one running, and which containers run an older image than the
// one already pulled.
package updates

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/client"
	"golang.org/x/sync/errgroup"
)

type Result struct {
	Ref       string
	Remote    string
	Local     []string // RepoDigests of the local image
	Available bool
	// Newer is a later tag of the same shape within the major version,
	// NewerMajor the latest with a higher major (1.27 → 1.29, 2.1).
	Newer, NewerMajor string
	Checked           time.Time
	Err               string
}

type Checker struct {
	api client.APIClient
	reg *registry
	// OnAvailable, when set, receives the images found newer since the
	// previous check (not on the first one: dockerr just started).
	OnAvailable func([]Result)
	// check looks one image up: checkOne, replaced in tests.
	check func(context.Context, string) Result

	mu      sync.RWMutex
	results map[string]Result
	running bool
	last    time.Time
}

func New(api client.APIClient, creds Credentials) *Checker {
	c := &Checker{api: api, reg: newRegistry(creds), results: map[string]Result{}}
	c.check = c.checkOne
	return c
}

// Get returns the last result for an image reference.
func (c *Checker) Get(ref string) (Result, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.results[ref]
	return r, ok
}

func (c *Checker) LastRun() (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.last, c.running
}

// Checkable reports whether a reference names a registry tag (not an image
// ID, a digest pin, or a local build).
func Checkable(ref string) bool {
	return ref != "" && !strings.HasPrefix(ref, "sha256:") && !strings.Contains(ref, "@")
}

// Check refreshes the given references, four at a time.
func (c *Checker) Check(ctx context.Context, refs []string) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.running, c.last = false, time.Now()
		c.mu.Unlock()
	}()

	c.mu.RLock()
	first := c.last.IsZero()
	c.mu.RUnlock()
	var fresh []Result
	seen := map[string]bool{}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	for _, ref := range refs {
		if !Checkable(ref) || seen[ref] {
			continue
		}
		seen[ref] = true
		g.Go(func() error {
			r := c.check(gctx, ref)
			c.mu.Lock()
			if r.Available && !c.results[ref].Available {
				fresh = append(fresh, r)
			}
			c.results[ref] = r
			c.mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	slog.Info("updates checked", "images", len(seen))
	if !first && len(fresh) > 0 && c.OnAvailable != nil {
		c.OnAvailable(fresh)
	}
}

func (c *Checker) checkOne(ctx context.Context, ref string) Result {
	r := Result{Ref: ref, Checked: time.Now()}
	img, err := c.api.ImageInspect(ctx, ref)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	r.Local = img.RepoDigests
	if len(r.Local) == 0 {
		r.Err = "image locale, sans registre"
		return r
	}
	repo, remote, err := c.reg.Digest(ctx, ref)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	r.Remote = remote
	if _, tag, ok := strings.Cut(strings.TrimPrefix(ref, repoPart(ref)), ":"); ok {
		if _, isVersion := parseVersion(tag); isVersion {
			if tags, err := c.reg.Tags(ctx, ref); err == nil {
				r.Newer, r.NewerMajor = newer(tag, tags)
			}
		}
	}
	r.Available = true
	for _, d := range r.Local {
		name, dg, _ := strings.Cut(d, "@")
		if dg == remote && (name == repo || strings.HasSuffix(name, "/"+repo) || strings.TrimPrefix(name, "docker.io/") == repo) {
			r.Available = false
		}
	}
	return r
}

// Loop checks refs() every interval until ctx ends.
func (c *Checker) Loop(ctx context.Context, interval time.Duration, refs func(context.Context) []string) {
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Check(ctx, refs(ctx))
			t.Reset(interval)
		}
	}
}

// repoPart is ref up to its tag: a registry port (host:5000/app:1) is not
// the tag.
func repoPart(ref string) string {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[:i+1]
	}
	return ""
}
