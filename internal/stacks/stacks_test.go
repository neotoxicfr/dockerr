package stacks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
)

func TestLabelFile(t *testing.T) {
	root := t.TempDir()
	pi, outside := filepath.Join(root, "pi"), filepath.Join(root, "outside")
	for _, d := range []string{filepath.Join(pi, "app"), filepath.Join(outside, "app")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(pi, "app", "compose.yaml"), filepath.Join(outside, "app", "compose.yaml")} {
		if err := os.WriteFile(f, []byte("services: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A link inside the folder leading out of it.
	if err := os.Symlink(filepath.Join(outside, "app"), filepath.Join(pi, "escape")); err != nil {
		t.Fatal(err)
	}
	inPi := filepath.Join(pi, "app", "compose.yaml")
	tests := []struct {
		name   string
		remote bool
		dir    string
		label  string
		want   string
	}{
		{name: "local", dir: "/mnt/docker", label: "/srv/app/compose.yaml,/srv/app/override.yaml", want: "/srv/app/compose.yaml"},
		{name: "no label", dir: "/mnt/docker"},
		{name: "remote without folder", remote: true, label: inPi},
		{name: "remote under folder", remote: true, dir: pi, label: inPi, want: inPi},
		{name: "remote elsewhere", remote: true, dir: pi, label: filepath.Join(outside, "app", "compose.yaml")},
		{name: "remote escape", remote: true, dir: pi, label: pi + "/../outside/app/compose.yaml"},
		{name: "remote escape through a link", remote: true, dir: pi, label: filepath.Join(pi, "escape", "compose.yaml")},
	}
	for _, tt := range tests {
		m := &Manager{Dir: tt.dir, Remote: tt.remote}
		if got := m.LabelFile(map[string]string{api.ConfigFilesLabel: tt.label}); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestCreateWithoutFolder(t *testing.T) {
	m := &Manager{Remote: true}
	if _, err := m.Create("app", "services: {}\n", ""); !errors.Is(err, ErrNoDir) {
		t.Fatalf("err = %v, want ErrNoDir", err)
	}
}
