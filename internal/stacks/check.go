package stacks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

// Host is what exists on the Docker host, for the checks that look beyond
// the compose file itself.
type Host struct {
	Networks   map[string]bool
	Volumes    map[string]bool
	Containers map[string]bool
	// Ports maps "8080/tcp" to the container publishing it.
	Ports map[string]PortUser
}

type PortUser struct{ Container, Project string }

// Report: Issues stop the stack from starting; Warnings let it start but
// probably not as intended.
type Report struct {
	Issues   []Issue `json:"issues"`
	Warnings []Issue `json:"warnings"`
}

// Check validates compose and .env as compose would, then looks for what
// would break at start: undefined variables, missing external networks and
// volumes, ports already taken, missing secret files and bind sources.
func Check(ctx context.Context, name, dir, composeText, envText string, host Host) Report {
	r := Report{Issues: []Issue{}, Warnings: []Issue{}}
	p, env, issues := load(ctx, name, dir, composeText, envText)
	if issues != nil {
		r.Issues = issues
		return r
	}
	at := func(path ...string) int { return lineOf(composeText, path) }

	for _, v := range undefinedVars(composeText, env) {
		r.Warnings = append(r.Warnings, Issue{Line: v.line, Message: fmt.Sprintf("variable %s non définie : elle vaudra une chaîne vide", v.name)})
	}
	for _, sname := range p.ServiceNames() {
		s := p.Services[sname]
		if ref, ok := strings.CutPrefix(s.NetworkMode, "container:"); ok && host.Containers != nil && !host.Containers[ref] {
			r.Warnings = append(r.Warnings, Issue{Line: at("services", sname, "network_mode"), Message: fmt.Sprintf("%s : le conteneur %s n'existe pas", sname, ref)})
		}
		for _, port := range s.Ports {
			if port.Published == "" {
				continue
			}
			proto := port.Protocol
			if proto == "" {
				proto = "tcp"
			}
			for _, pub := range expandRange(port.Published) {
				if u, ok := host.Ports[pub+"/"+proto]; ok && u.Project != p.Name {
					r.Warnings = append(r.Warnings, Issue{Line: at("services", sname, "ports"),
						Message: fmt.Sprintf("%s : le port %s/%s est déjà pris par %s", sname, pub, proto, u.Container)})
				}
			}
		}
		for _, v := range s.Volumes {
			if v.Type != types.VolumeTypeBind || v.Source == "" {
				continue
			}
			src := v.Source
			if !filepath.IsAbs(src) {
				src = filepath.Join(dir, src)
			}
			if _, err := os.Stat(src); os.IsNotExist(err) && dirVisible(dir) {
				r.Warnings = append(r.Warnings, Issue{Line: at("services", sname, "volumes"),
					Message: fmt.Sprintf("%s : %s n'existe pas, Docker créera un dossier vide", sname, v.Source)})
			}
		}
	}
	for key, n := range p.Networks {
		if bool(n.External) && host.Networks != nil && !host.Networks[orName(n.Name, key)] {
			r.Issues = append(r.Issues, Issue{Line: at("networks", key), Message: fmt.Sprintf("réseau externe %s introuvable", orName(n.Name, key))})
		}
	}
	for key, v := range p.Volumes {
		if bool(v.External) && host.Volumes != nil && !host.Volumes[orName(v.Name, key)] {
			r.Issues = append(r.Issues, Issue{Line: at("volumes", key), Message: fmt.Sprintf("volume externe %s introuvable", orName(v.Name, key))})
		}
	}
	for key, s := range p.Secrets {
		if bool(s.External) || s.File == "" {
			continue
		}
		f := s.File
		if !filepath.IsAbs(f) {
			f = filepath.Join(dir, f)
		}
		if _, err := os.Stat(f); os.IsNotExist(err) && dirVisible(dir) {
			r.Issues = append(r.Issues, Issue{Line: at("secrets", key), Message: fmt.Sprintf("secret %s : le fichier %s n'existe pas", key, s.File)})
		}
	}
	byLine := func(a, b Issue) int { return a.Line - b.Line }
	slices.SortStableFunc(r.Issues, byLine)
	slices.SortStableFunc(r.Warnings, byLine)
	return r
}

func orName(name, key string) string {
	if name != "" {
		return name
	}
	return key
}

// dirVisible: a new stack's folder does not exist yet, so its relative paths
// cannot be checked.
func dirVisible(dir string) bool {
	_, err := os.Stat(dir)
	return err == nil
}

// expandRange turns "8000-8002" into its ports; other values pass through.
func expandRange(s string) []string {
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return []string{s}
	}
	var lo, hi int
	if _, err := fmt.Sscan(a, &lo); err != nil {
		return []string{s}
	}
	if _, err := fmt.Sscan(b, &hi); err != nil || hi < lo || hi-lo > 1000 {
		return []string{s}
	}
	out := make([]string, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, fmt.Sprint(i))
	}
	return out
}

// ${VAR} and $VAR, but not $$ (escaped) nor ${VAR:-default}-style forms.
var varRe = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)([^}]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

type undefinedVar struct {
	name string
	line int
}

func undefinedVars(text string, env map[string]string) []undefinedVar {
	var out []undefinedVar
	seen := map[string]bool{}
	for i, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, m := range varRe.FindAllStringSubmatch(line, -1) {
			if m[0] == "$$" {
				continue
			}
			name, mod := m[1], m[2]
			if name == "" {
				name = m[3]
			}
			// A default (:- or -) or a required error (:? or ?) handles the unset case.
			if strings.HasPrefix(mod, ":-") || strings.HasPrefix(mod, "-") || strings.HasPrefix(mod, ":?") || strings.HasPrefix(mod, "?") ||
				strings.HasPrefix(mod, ":+") || strings.HasPrefix(mod, "+") {
				continue
			}
			if _, ok := env[name]; ok || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, undefinedVar{name, i + 1})
		}
	}
	return out
}
