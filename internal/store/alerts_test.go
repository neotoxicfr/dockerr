package store

import (
	"context"
	"testing"
)

func TestLogAlerts(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, a := range []LogAlert{{Pattern: "ERROR", By: "u"}, {Pattern: "/panic/", Stack: "web", By: "u"}} {
		if err := s.AddLogAlert(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.LogAlerts(ctx)
	if err != nil || len(list) != 2 || list[1].Stack != "web" || list[0].Pattern != "ERROR" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := s.DeleteLogAlert(ctx, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.LogAlerts(ctx); len(list) != 1 || list[0].Pattern != "/panic/" {
		t.Fatalf("after delete = %+v", list)
	}
}
