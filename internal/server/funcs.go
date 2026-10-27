package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/neotoxicfr/dockerr/internal/stacks"
)

var funcs = template.FuncMap{
	"bytes":      humanBytes,
	"cpu":        func(f float64) string { return fmt.Sprintf("%.1f %%", f) },
	"since":      since,
	"datetime":   func(t time.Time) string { return t.Local().Format("02/01/2006 15:04") },
	"short":      func(s string) string { s = strings.TrimPrefix(s, "sha256:"); return s[:min(12, len(s))] },
	"stateLabel": stateLabel,
	"ctrLabel":   containerLabel,
	"ctrClass":   containerClass,
	"imageTag":   imageTag,
	"netMode": func(m string) string {
		if ref, ok := strings.CutPrefix(m, "container:"); ok && len(ref) == 64 {
			return "container:" + ref[:12]
		}
		return m
	},
	"json": func(v any) (string, error) {
		b, err := json.MarshalIndent(v, "", "  ")
		return string(b), err
	},
	"join": strings.Join,
	"base": filepath.Base,
	"u64":  func(n int64) uint64 { return uint64(max(n, 0)) },
	"add":  func(a, b int) int { return a + b },
	"has":  func(list []string, s string) bool { return slices.Contains(list, s) },
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
	"plural": func(n int, one, many string) string {
		if n > 1 {
			return many
		}
		return one
	},
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d o", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	f := "%.1f %cio"
	if v >= 100 {
		f = "%.0f %cio"
	}
	return fmt.Sprintf(f, v, "KMGTPE"[exp])
}

func since(t time.Time) string {
	if t.IsZero() {
		return "jamais"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "à l'instant"
	case d < time.Hour:
		return fmt.Sprintf("il y a %d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("il y a %d h", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("il y a %d j", int(d.Hours()/24))
	}
	return t.Local().Format("02/01/2006")
}

func stateLabel(s stacks.State) string {
	switch s {
	case stacks.Running:
		return "En marche"
	case stacks.Partial:
		return "Partielle"
	case stacks.Problem:
		return "Problème"
	}
	return "Arrêtée"
}

func containerLabel(c *stacks.Container) string {
	switch {
	case c.NetLost:
		return "Réseau perdu"
	case c.State == "running" && c.Health == "unhealthy":
		return "En échec"
	case c.State == "running" && c.Health == "starting":
		return "Démarre"
	case c.State == "running":
		return "En marche"
	case c.State == "restarting":
		return "Redémarre en boucle"
	case c.State == "paused":
		return "En pause"
	case c.State == "created":
		return "Créé"
	case c.State == "dead":
		return "Mort"
	case c.State == "exited" && c.ExitCode == 0:
		return "Terminé"
	case c.State == "exited":
		return fmt.Sprintf("Arrêté (%d)", c.ExitCode)
	}
	return c.State
}

func containerClass(c *stacks.Container) string {
	switch {
	case c.NetLost, c.Health == "unhealthy", c.State == "restarting", c.State == "dead":
		return "bad"
	case c.State == "running" && c.Health == "starting":
		return "warn"
	case c.State == "running":
		return "ok"
	}
	return "off"
}

// imageTag shortens "ghcr.io/org/app:1.2" to "app:1.2".
func imageTag(ref string) string {
	if strings.HasPrefix(ref, "sha256:") {
		return ref[7:19]
	}
	ref, _, _ = strings.Cut(ref, "@")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	return ref
}
