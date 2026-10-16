package stacks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	ok := "services:\n  web:\n    image: nginx:${TAG}\n    ports:\n      - \"8080:80\"\n"
	if p := Validate(context.Background(), "demo", dir, ok, "TAG=1.27\n"); p != nil {
		t.Fatalf("valid file: %v", p)
	}

	bad := "services:\n  web:\n    image: nginx\n   ports:\n      - 80\n"
	p := Validate(context.Background(), "demo", dir, bad, "")
	if len(p) != 1 || p[0].Line == 0 {
		t.Fatalf("yaml error without line: %+v", p)
	}

	schema := "services:\n  web:\n    image: nginx\n    restart: 12\n    portz:\n      - 80\n"
	p = Validate(context.Background(), "demo", dir, schema, "")
	if len(p) != 1 {
		t.Fatalf("schema error: %+v", p)
	}
	if p[0].Line == 0 {
		t.Logf("no line for %q", p[0].Message)
	}
	if strings.Contains(p[0].Message, dir) {
		t.Errorf("message leaks temp path: %q", p[0].Message)
	}
}

func TestLineOf(t *testing.T) {
	text := "services:\n  web:\n    image: nginx\n    ports:\n      - 80\n      - 81\n"
	if l := lineOf(text, []string{"services", "web", "ports", "1"}); l != 6 {
		t.Fatalf("line %d, want 6", l)
	}
	if l := lineOf(text, []string{"services", "web", "nope"}); l != 2 {
		t.Fatalf("line %d, want 2", l)
	}
}

func TestWriteFilesKeepsMode(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(f, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &Stack{Dir: dir, File: f, Managed: true}
	if err := st.WriteFiles("services: {}\n", ""); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(f)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if _, err := os.Stat(st.EnvPath()); !os.IsNotExist(err) {
		t.Fatalf(".env created for empty content")
	}
	if err := st.WriteFiles("services: {}\n", "A=1\n"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(st.EnvPath()); string(b) != "A=1\n" {
		t.Fatalf(".env = %q", b)
	}
}

func TestProjectName(t *testing.T) {
	for in, want := range map[string]string{"wake-pc": "wake-pc", "My_App": "my_app", "-x.y": "xy"} {
		if got := projectName(in); got != want {
			t.Errorf("projectName(%q) = %q, want %q", in, got, want)
		}
	}
}
