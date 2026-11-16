package stacks

import (
	"errors"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
)

func TestLabelFile(t *testing.T) {
	tests := []struct {
		name   string
		remote bool
		dir    string
		label  string
		want   string
	}{
		{name: "local", dir: "/mnt/docker", label: "/srv/app/compose.yaml,/srv/app/override.yaml", want: "/srv/app/compose.yaml"},
		{name: "no label", dir: "/mnt/docker"},
		{name: "remote without folder", remote: true, label: "/mnt/docker/app/compose.yaml"},
		{name: "remote under folder", remote: true, dir: "/mnt/pi", label: "/mnt/pi/app/compose.yaml", want: "/mnt/pi/app/compose.yaml"},
		{name: "remote elsewhere", remote: true, dir: "/mnt/pi", label: "/mnt/docker/app/compose.yaml"},
		{name: "remote escape", remote: true, dir: "/mnt/pi", label: "/mnt/pi/../docker/app/compose.yaml"},
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
