package stacks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"go.yaml.in/yaml/v4"
)

// EnvPath is the .env file next to the compose file.
func (st *Stack) EnvPath() string { return filepath.Join(st.Dir, ".env") }

// ReadFiles returns the compose file and .env contents ("" when absent).
func (st *Stack) ReadFiles() (composeText, envText string, err error) {
	if st.File == "" {
		return "", "", errors.New("fichier compose introuvable")
	}
	b, err := os.ReadFile(st.File)
	if err != nil {
		return "", "", err
	}
	e, err := os.ReadFile(st.EnvPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	return string(b), string(e), nil
}

// Issue is a validation error, with its line when it can be found (0 otherwise).
type Issue struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

var (
	lineRe = regexp.MustCompile(`line (\d+)|L(\d+)\.C\d+(?:-L(\d+))?`)
	pathRe = regexp.MustCompile(`\b((?:services|networks|volumes|configs|secrets)(?:\.[A-Za-z0-9_\-]+)+)`)
)

// Validate loads compose and env contents the way `docker compose` would,
// without touching the files on disk.
func Validate(ctx context.Context, name, dir, composeText, envText string) []Issue {
	_, _, issues := load(ctx, name, dir, composeText, envText)
	return issues
}

// load parses compose and .env contents the way compose does, without paths
// resolution, and returns the project with the .env variables.
func load(ctx context.Context, name, dir, composeText, envText string) (*types.Project, map[string]string, []Issue) {
	env := map[string]string{}
	if strings.TrimSpace(envText) != "" {
		e, err := dotenv.UnmarshalBytesWithLookup([]byte(envText), nil)
		if err != nil {
			return nil, nil, []Issue{{Message: ".env : " + err.Error()}}
		}
		env = e
	}
	file := filepath.Join(dir, "compose.yaml")
	p, err := loader.LoadWithContext(ctx, types.ConfigDetails{
		WorkingDir:  dir,
		ConfigFiles: []types.ConfigFile{{Filename: file, Content: []byte(composeText)}},
		Environment: env,
	}, func(o *loader.Options) {
		o.SetProjectName(name, true)
		o.ResolvePaths = false
	})
	if err == nil {
		return p, env, nil
	}
	msg := strings.TrimPrefix(err.Error(), "validating "+file+": ")
	msg = strings.ReplaceAll(msg, file+": ", "")
	is := Issue{Message: msg}
	if m := lineRe.FindStringSubmatch(msg); m != nil {
		for _, g := range []string{m[3], m[2], m[1]} {
			if g != "" {
				is.Line, _ = strconv.Atoi(g)
				break
			}
		}
		// "failed to parse … at L2.C3-L4.C4: did not find expected key"
		if i := strings.LastIndex(msg, ": "); i >= 0 {
			is.Message = msg[i+2:]
		}
	} else if m := pathRe.FindStringSubmatch(msg); m != nil {
		is.Line = lineOf(composeText, strings.Split(m[1], "."))
	}
	return nil, env, []Issue{is}
}

// lineOf returns the line of the deepest YAML node found along path.
func lineOf(text string, path []string) int {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(text), &doc) != nil || len(doc.Content) == 0 {
		return 0
	}
	n, line := doc.Content[0], 0
	for _, key := range path {
		var next *yaml.Node
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == key {
					line, next = n.Content[i].Line, n.Content[i+1]
					break
				}
			}
		case yaml.SequenceNode:
			if i, err := strconv.Atoi(key); err == nil && i < len(n.Content) {
				next = n.Content[i]
				line = next.Line
			}
		}
		if next == nil {
			break
		}
		n = next
	}
	return line
}

// WriteFiles replaces the compose and .env files. An empty envText removes
// .env only if it did not exist before.
func (st *Stack) WriteFiles(composeText, envText string) error {
	if !st.Managed || st.File == "" {
		return errors.New("pile hors du dossier des piles : lecture seule")
	}
	if err := writeAtomic(st.File, []byte(composeText)); err != nil {
		return err
	}
	_, statErr := os.Stat(st.EnvPath())
	if envText == "" && errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	return writeAtomic(st.EnvPath(), []byte(envText))
}

// writeAtomic writes through a temporary file and a rename, keeping the
// original mode and, when allowed, its owner.
func writeAtomic(path string, data []byte) error { return writeAtomicMode(path, data, 0o644, -1) }

// writeAtomicMode uses mode for a new file; an existing file keeps its own.
// owner >= 0 sets the file's uid and gid, otherwise an existing file keeps
// its owner. Mode and owner are set on the open temporary file, before the
// rename: never through a path someone else could swap for a link.
func writeAtomicMode(path string, data []byte, mode os.FileMode, owner int) error {
	var uid, gid = -1, -1
	if st, err := os.Lstat(path); err == nil {
		if !st.Mode().IsRegular() {
			return fmt.Errorf("%s n'est pas un fichier ordinaire", filepath.Base(path))
		}
		mode = st.Mode().Perm()
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(sys.Uid), int(sys.Gid)
		}
		if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) && (owner < 0 || owner == uid) {
			return nil
		}
	}
	if owner >= 0 {
		uid, gid = owner, owner
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".dockerr-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	fail := func(err error) error { tmp.Close(); return err }
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if uid >= 0 {
		if err := tmp.Chown(uid, gid); err != nil && owner >= 0 {
			return fail(err) // asked for: must not silently keep root
		}
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Create makes a new stack folder with its compose file.
func (m *Manager) Create(name, composeText, envText string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("nom invalide : minuscules, chiffres, - et _")
	}
	dir := filepath.Join(m.Dir, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("le dossier %s existe déjà", name)
		}
		return "", err
	}
	file := filepath.Join(dir, "compose.yaml")
	if err := writeAtomic(file, []byte(composeText)); err != nil {
		return "", err
	}
	if envText != "" {
		if err := writeAtomic(filepath.Join(dir, ".env"), []byte(envText)); err != nil {
			return "", err
		}
	}
	return file, nil
}
