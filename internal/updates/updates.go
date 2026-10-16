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
	Checked   time.Time
	Err       string
}

type Checker struct {
	api client.APIClient
	reg *registry

	mu      sync.RWMutex
	results map[string]Result
	running bool
	last    time.Time
}

func New(api client.APIClient, creds Credentials) *Checker {
	return &Checker{api: api, reg: newRegistry(creds), results: map[string]Result{}}
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

	seen := map[string]bool{}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	for _, ref := range refs {
		if !Checkable(ref) || seen[ref] {
			continue
		}
		seen[ref] = true
		g.Go(func() error {
			r := c.checkOne(gctx, ref)
			c.mu.Lock()
			c.results[ref] = r
			c.mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	slog.Info("updates checked", "images", len(seen))
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
