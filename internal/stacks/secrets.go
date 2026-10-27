package stacks

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var secretNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// ValidSecretName reports whether s can name a secret and its file.
func ValidSecretName(s string) bool { return secretNameRe.MatchString(s) }

// ErrOutside: dockerr only reads and writes secret files inside the stack folder.
var ErrOutside = errors.New("le fichier du secret est hors du dossier de la pile")

// SecretPath resolves a compose secret's file against the stack folder and
// refuses anything that leaves it, symbolic links included.
func SecretPath(dir, file string) (string, error) {
	if dir == "" || file == "" {
		return "", ErrOutside
	}
	p := file
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	p = filepath.Clean(p)
	base, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	// Resolve the deepest existing parent: a link inside the stack folder
	// must not lead out of it.
	real := p
	for probe := p; ; probe = filepath.Dir(probe) {
		if r, err := filepath.EvalSymlinks(probe); err == nil {
			rel, _ := filepath.Rel(probe, p)
			real = filepath.Join(r, rel)
			break
		}
		if probe == filepath.Dir(probe) {
			break
		}
	}
	rel, err := filepath.Rel(base, real)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrOutside
	}
	return real, nil
}

// ReadSecret returns a secret file's content ("" when it does not exist yet).
func ReadSecret(dir, file string) (string, error) {
	p, err := SecretPath(dir, file)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// WriteSecret writes a secret file readable by its owner only. Compose
// bind-mounts file secrets as they are (uid/mode in the service are ignored
// for them), so a container not running as root reads the file only if it
// owns it: uid >= 0 gives the file to that user (and group), -1 keeps the
// current owner.
func WriteSecret(dir, file, value string, uid int) error {
	p, err := SecretPath(dir, file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return writeAtomicMode(p, []byte(value), 0o600, uid)
}

// SecretOwner returns the uid owning a secret file, -1 when it does not exist.
func SecretOwner(dir, file string) int {
	p, err := SecretPath(dir, file)
	if err != nil {
		return -1
	}
	st, err := os.Stat(p)
	if err != nil {
		return -1
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(sys.Uid)
	}
	return -1
}

// RemoveSecret deletes a secret file; a missing file is not an error.
func RemoveSecret(dir, file string) error {
	p, err := SecretPath(dir, file)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
