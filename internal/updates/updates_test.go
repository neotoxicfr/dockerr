package updates

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestOnAvailable(t *testing.T) {
	avail := map[string]bool{"a:1": true, "b:1": false}
	var mu sync.Mutex
	c := &Checker{results: map[string]Result{}}
	c.check = func(_ context.Context, ref string) Result {
		mu.Lock()
		defer mu.Unlock()
		return Result{Ref: ref, Available: avail[ref]}
	}
	var got [][]string
	c.OnAvailable = func(rs []Result) {
		var refs []string
		for _, r := range rs {
			refs = append(refs, r.Ref)
		}
		got = append(got, refs)
	}
	refs := []string{"a:1", "b:1"}
	c.Check(context.Background(), refs) // first run: dockerr just started, nothing sent
	c.Check(context.Background(), refs) // nothing new
	mu.Lock()
	avail["b:1"] = true
	mu.Unlock()
	c.Check(context.Background(), refs) // b is new, a was already known
	if len(got) != 1 || len(got[0]) != 1 || got[0][0] != "b:1" {
		t.Fatalf("OnAvailable calls = %v, want [[b:1]]", got)
	}
}

func TestTransientErrorIsNotNew(t *testing.T) {
	step := 0
	c := &Checker{results: map[string]Result{}}
	c.check = func(_ context.Context, ref string) Result {
		if step == 2 {
			return Result{Ref: ref, Err: "timeout"}
		}
		return Result{Ref: ref, Available: step >= 1}
	}
	var calls int
	c.OnAvailable = func([]Result) { calls++ }
	for step = 0; step < 4; step++ { // known, new, registry error, available again
		c.Check(context.Background(), []string{"a:1"})
	}
	if calls != 1 {
		t.Fatalf("announced %d times, want 1", calls)
	}
}

func TestCheckWhenIdle(t *testing.T) {
	newChecker := func(running bool) *Checker {
		c := &Checker{results: map[string]Result{}, running: running}
		c.check = func(_ context.Context, ref string) Result { return Result{Ref: ref} }
		return c
	}
	cases := []struct {
		name        string
		running     bool          // a check already runs
		endsAfter   time.Duration // when it ends (0: never)
		cancelFirst bool
		want        bool
	}{
		{"idle runs at once", false, 0, false, true},
		{"waits for the running check", true, 300 * time.Millisecond, false, true},
		{"cancelled while waiting", true, 0, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch := newChecker(c.running)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if c.cancelFirst {
				cancel()
			}
			if c.endsAfter > 0 {
				go func() {
					time.Sleep(c.endsAfter)
					ch.mu.Lock()
					ch.running = false
					ch.mu.Unlock()
				}()
			}
			if got := ch.CheckWhenIdle(ctx, []string{"a:1"}); got != c.want {
				t.Fatalf("CheckWhenIdle = %v, want %v", got, c.want)
			}
			if _, ok := ch.Get("a:1"); ok != c.want {
				t.Errorf("checked = %v, want %v", ok, c.want)
			}
		})
	}
}
