package store

import (
	"context"
	"testing"
	"time"
)

func TestOps(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	for _, o := range []Operation{
		{ID: "old", Stack: "a", Kind: "start", User: "u", Started: now.Add(-100 * 24 * time.Hour), Ended: now.Add(-100 * 24 * time.Hour)},
		{ID: "1", Stack: "a", Kind: "update", User: "u", Started: now.Add(-2 * time.Minute), Ended: now.Add(-time.Minute),
			Tasks: []OpTask{{Text: "Container a-web-1", Details: "Started", Status: "done"}}},
		{ID: "2", Stack: "b", Kind: "stop", User: "v", Started: now.Add(-time.Minute), Ended: now, Err: "boom"},
	} {
		if err := s.SaveOp(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.Ops(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != "2" || all[1].ID != "1" {
		t.Fatalf("all = %+v (want 2 then 1, old pruned)", all)
	}
	if all[0].Err != "boom" || len(all[1].Tasks) != 1 || all[1].Tasks[0].Details != "Started" || all[1].Duration() != time.Minute {
		t.Fatalf("fields lost: %+v", all)
	}
	a, err := s.Ops(ctx, "a", 10)
	if err != nil || len(a) != 1 || a[0].ID != "1" {
		t.Fatalf("stack a = %+v, %v", a, err)
	}
}
