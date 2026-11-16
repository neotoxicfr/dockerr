package stacks

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/neotoxicfr/dockerr/internal/composeedit"
)

// Fix is a one-click correction: graphical edits on the compose file and,
// when a value moves out of it, lines to append to .env.
type Fix struct {
	Label     string             `json:"label"`
	Edits     []composeedit.Edit `json:"edits"`
	EnvAppend string             `json:"env_append,omitempty"`
}

// SensitiveKey matches environment variables that likely hold a secret.
var SensitiveKey = regexp.MustCompile(`(?i)pass|secret|token|key|pwd|auth|cred|private|salt`)

// Ports databases listen on: published on every interface, they are open
// to the whole network.
var dbPorts = map[string]bool{"5432": true, "3306": true, "27017": true, "6379": true, "9200": true,
	"11211": true, "5984": true, "8086": true, "9042": true, "26257": true, "1433": true, "1521": true}

var notSecret = map[string]bool{"true": true, "false": true, "yes": true, "no": true, "on": true, "off": true}

// lint warns about what starts fine but is risky or outdated, with a fix
// when the correction is unambiguous.
func lint(p *types.Project, composeText string, env map[string]string, at func(...string) int) []Issue {
	var out []Issue
	doc, err := composeedit.Parse(composeText)
	if err != nil {
		return nil
	}
	raw := map[string]composeedit.Service{}
	for _, s := range doc.Model().Services {
		raw[s.Name] = s
	}
	if l := at("version"); l > 0 {
		out = append(out, Issue{Line: l, Message: "la clé version est obsolète : Compose l'ignore",
			Fix: &Fix{Label: "Retirer", Edits: []composeedit.Edit{{Op: "remove", Path: []string{"version"}}}}})
	}
	for _, name := range p.ServiceNames() {
		s, r := p.Services[name], raw[name]
		if s.Privileged {
			out = append(out, Issue{Line: at("services", name, "privileged"),
				Message: name + " : privileged donne au conteneur un accès complet à l'hôte"})
		}
		for _, v := range s.Volumes {
			if v.Type == types.VolumeTypeBind && (v.Source == "/var/run/docker.sock" || v.Source == "/run/docker.sock") &&
				!strings.Contains(s.Image, "socket-proxy") {
				out = append(out, Issue{Line: at("services", name, "volumes"),
					Message: name + " : le socket Docker monté vaut un accès root à l'hôte ; préfère un proxy de socket"})
			}
		}
		if s.Image != "" && unpinned(s.Image) {
			out = append(out, Issue{Line: at("services", name, "image"),
				Message: fmt.Sprintf("%s : %s n'a pas de version fixée, une mise à jour change ce qui tourne", name, s.Image)})
		}
		out = append(out, exposedDB(name, r, at)...)
		out = append(out, hardcoded(name, r, env, at)...)
	}
	return out
}

// unpinned reports an image without tag, or tagged latest, and no digest.
func unpinned(image string) bool {
	if strings.Contains(image, "@") {
		return false
	}
	last := image[strings.LastIndex(image, "/")+1:]
	i := strings.LastIndex(last, ":")
	return i < 0 || last[i+1:] == "latest"
}

// exposedDB warns about a database port published on every interface, and
// binds it to 127.0.0.1 when every entry is short syntax.
func exposedDB(name string, s composeedit.Service, at func(...string) int) []Issue {
	var out []Issue
	fixable := !locked(s, "ports")
	next := make([]string, len(s.Ports))
	changed := false
	for i, p := range s.Ports {
		next[i] = p.Value
		if p.Long {
			fixable = false
			continue
		}
		spec, _, _ := strings.Cut(p.Value, "/")
		parts := strings.Split(spec, ":")
		if len(parts) != 2 || !dbPorts[parts[1]] {
			continue // no published port, or already bound to an address
		}
		next[i] = "127.0.0.1:" + p.Value
		changed = true
		out = append(out, Issue{Line: at("services", name, "ports"),
			Message: fmt.Sprintf("%s : le port %s est ouvert sur toutes les interfaces ; une base de données devrait écouter sur 127.0.0.1", name, parts[0])})
	}
	if changed && fixable {
		v, _ := json.Marshal(next)
		fix := &Fix{Label: "Limiter à 127.0.0.1", Edits: []composeedit.Edit{{Op: "list", Path: []string{"services", name, "ports"}, Value: v}}}
		for i := range out {
			out[i].Fix = fix
		}
	}
	return out
}

// hardcoded warns about a secret-looking variable written in the compose
// file, and moves it to .env when it is not there yet.
func hardcoded(name string, s composeedit.Service, env map[string]string, at func(...string) int) []Issue {
	var out []Issue
	for i, kv := range s.Environment {
		v := strings.TrimSpace(kv.Value)
		if !SensitiveKey.MatchString(kv.Key) || v == "" || strings.Contains(v, "$") || notSecret[strings.ToLower(v)] {
			continue
		}
		is := Issue{Line: at("services", name, "environment"),
			Message: fmt.Sprintf("%s : %s est écrit en clair dans le compose ; mets-le dans .env", name, kv.Key)}
		if line, ok := envLine(kv.Key, kv.Value); ok && !locked(s, "environment") {
			if _, taken := env[kv.Key]; !taken {
				next := append([]composeedit.KV(nil), s.Environment...)
				next[i].Value = "${" + kv.Key + "}"
				v, _ := json.Marshal(next)
				is.Fix = &Fix{Label: "Déplacer dans .env", EnvAppend: line,
					Edits: []composeedit.Edit{{Op: "map", Path: []string{"services", name, "environment"}, Value: v}}}
			}
		}
		out = append(out, is)
	}
	return out
}

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// envLine writes KEY=value for .env, quoting when needed; ok is false when
// the value cannot be written back safely.
func envLine(key, value string) (string, bool) {
	if !envKeyRe.MatchString(key) || strings.ContainsAny(value, "\n\r") {
		return "", false
	}
	if strings.ContainsAny(value, " \t#\"'\\$`") {
		if strings.Contains(value, "'") {
			return "", false
		}
		value = "'" + value + "'"
	}
	return key + "=" + value + "\n", true
}

func locked(s composeedit.Service, field string) bool {
	for _, f := range s.Locked {
		if f == field {
			return true
		}
	}
	return false
}
