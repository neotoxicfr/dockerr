package store

import (
	"context"
	"testing"
	"time"
)

func TestOpCounts(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		ops  []Operation
		want []OpCount
	}{
		{"empty", nil, nil},
		{"by kind and result", []Operation{
			{ID: "1", Stack: "a", Kind: "update", Started: now, Ended: now},
			{ID: "2", Stack: "b", Kind: "update", Started: now, Ended: now},
			{ID: "3", Stack: "b", Kind: "update", Started: now, Ended: now, Err: "boom"},
			{ID: "4", Stack: "b", Kind: "stop", Started: now, Ended: now},
		}, []OpCount{{"stop", false, 1}, {"update", false, 2}, {"update", true, 1}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for _, o := range c.ops {
				if err := s.SaveOp(ctx, o); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.OpCounts(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("%v, want %v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("%d: %v, want %v", i, got[i], c.want[i])
				}
			}
		})
	}
}

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
		{ID: "pi", Host: "pi", Stack: "a", Kind: "stop", User: "v", Started: now.Add(-3 * time.Minute), Ended: now.Add(-3 * time.Minute)},
	} {
		if err := s.SaveOp(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.Ops(ctx, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ID != "2" || all[1].ID != "1" || all[2].Host != "pi" {
		t.Fatalf("all = %+v (want 2, 1, pi, old pruned)", all)
	}
	if p, err := s.Ops(ctx, "pi", "a", 10); err != nil || len(p) != 1 || p[0].ID != "pi" {
		t.Fatalf("stack a on pi = %+v, %v", p, err)
	}
	if all[0].Err != "boom" || len(all[1].Tasks) != 1 || all[1].Tasks[0].Details != "Started" || all[1].Duration() != time.Minute {
		t.Fatalf("fields lost: %+v", all)
	}
	a, err := s.Ops(ctx, "", "a", 10)
	if err != nil || len(a) != 1 || a[0].ID != "1" {
		t.Fatalf("stack a = %+v, %v", a, err)
	}
}
