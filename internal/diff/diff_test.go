package diff

import "testing"

func TestLines(t *testing.T) {
	got := Lines("a\nb\nc\n", "a\nx\nc\nd\n")
	want := "  a\n- b\n+ x\n  c\n+ d\n"
	s := ""
	for _, l := range got {
		s += string(l.Op) + " " + l.Text + "\n"
	}
	if s != want {
		t.Fatalf("got\n%s\nwant\n%s", s, want)
	}
	if Changed(Lines("a\n", "a\n")) {
		t.Fatal("identical inputs reported as changed")
	}
	h := Hunks(Lines("1\n2\n3\n4\n5\n6\n7\n8\n", "1\n2\n3\n4\n5\n6\n7\nX\n"), 1)
	if len(h) != 3 || h[0].Text != "7" {
		t.Fatalf("hunks: %d lines, first %+v", len(h), h[0])
	}
}
