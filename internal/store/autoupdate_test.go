package store

import (
	"context"
	"slices"
	"testing"
)

func TestAutoUpdates(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, n := range []string{"web", "db", "web"} {
		if err := s.SetAutoUpdate(ctx, n, true, "u"); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.AutoUpdates(ctx); err != nil || !slices.Equal(got, []string{"db", "web"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if err := s.SetAutoUpdate(ctx, "db", false, "u"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AutoUpdates(ctx); !slices.Equal(got, []string{"web"}) {
		t.Fatalf("after off: %v", got)
	}
}
