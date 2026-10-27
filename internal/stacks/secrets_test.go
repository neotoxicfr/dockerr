package stacks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSecretPathEscape(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "stack")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{dir, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"../outside/x", "/etc/shadow", "link/x", "", ".", "secrets/../../outside/y"} {
		if _, err := SecretPath(dir, f); !errors.Is(err, ErrOutside) {
			t.Errorf("%q: err = %v, want ErrOutside", f, err)
		}
	}
	if err := WriteSecret(dir, "link/x", "v"); !errors.Is(err, ErrOutside) {
		t.Errorf("write through link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "x")); err == nil {
		t.Fatal("file written outside the stack folder")
	}
}

func TestSecretReadWrite(t *testing.T) {
	dir := t.TempDir()
	if v, err := ReadSecret(dir, "./secrets/db"); err != nil || v != "" {
		t.Fatalf("missing secret: %q %v", v, err)
	}
	if err := WriteSecret(dir, "./secrets/db", "s3cret"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "secrets", "db"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi.Mode(), err)
	}
	if v, _ := ReadSecret(dir, "secrets/db"); v != "s3cret" {
		t.Fatalf("read %q", v)
	}
	if err := RemoveSecret(dir, "secrets/db"); err != nil {
		t.Fatal(err)
	}
	if !ValidSecretName("db_password.v2") || ValidSecretName("../x") || ValidSecretName("") {
		t.Error("ValidSecretName")
	}
}
