package updates

import (
	"context"
	"sync"
	"testing"
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
