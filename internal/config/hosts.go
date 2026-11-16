package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Host is a Docker host reached over TCP, besides the local one.
type Host struct {
	Name string
	URL  string // tcp://host:port
	// TLSDir holds ca.pem, cert.pem and key.pem; empty: plain TCP, which
	// gives root on that machine to anyone on the network path.
	TLSDir string
	// StacksDir is where this host's compose folders are mounted here, at
	// the same path as on the host (relative paths resolve against it).
	// Empty: its stacks are known from their containers only.
	StacksDir string
	// PortHost is the name used in links to published ports.
	PortHost string
}

var hostNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// TLSFiles are the files a host's TLS directory must hold, as Docker names them.
var TLSFiles = []string{"ca.pem", "cert.pem", "key.pem"}

// hostEnv turns a host name into its variables' infix: "nas-2" → "NAS_2".
func hostEnv(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// parseHosts reads DOCKERR_HOSTS ("pi=tcp://pi.example:2376,nas2=tcp://…")
// and each host's DOCKERR_TLS_<NAME>_DIR (DOCKER_CERT_PATH by default),
// DOCKERR_STACKS_DIR_<NAME> and DOCKERR_PORT_HOST_<NAME>.
func parseHosts(getenv func(string) string, local string) ([]Host, error) {
	var out []Host
	seen := map[string]bool{local: true}
	for _, item := range strings.FieldsFunc(getenv("DOCKERR_HOSTS"), func(r rune) bool { return r == ',' || r == ' ' }) {
		name, raw, ok := strings.Cut(item, "=")
		if !ok || !hostNameRe.MatchString(name) {
			return nil, fmt.Errorf("DOCKERR_HOSTS : %q n'est pas de la forme nom=tcp://hôte:port (nom en minuscules, chiffres, - et _)", item)
		}
		if seen[name] {
			return nil, fmt.Errorf("DOCKERR_HOSTS : le nom %q est déjà pris", name)
		}
		seen[name] = true
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "tcp" || u.Hostname() == "" || u.Port() == "" || u.User != nil ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("DOCKERR_HOSTS : %s : adresse %q invalide, attendu tcp://hôte:port", name, raw)
		}
		h := Host{Name: name, URL: "tcp://" + u.Host, PortHost: u.Hostname()}
		e := hostEnv(name)
		h.TLSDir = getenv("DOCKERR_TLS_" + e + "_DIR")
		if h.TLSDir == "" {
			h.TLSDir = getenv("DOCKER_CERT_PATH")
		}
		if h.TLSDir != "" {
			for _, f := range TLSFiles {
				if _, err := os.Stat(filepath.Join(h.TLSDir, f)); err != nil {
					return nil, fmt.Errorf("TLS de l'hôte %s : %w", name, err)
				}
			}
		}
		h.StacksDir = getenv("DOCKERR_STACKS_DIR_" + e)
		if p := getenv("DOCKERR_PORT_HOST_" + e); p != "" {
			h.PortHost = p
		}
		if ip := net.ParseIP(h.PortHost); ip != nil && ip.To4() == nil {
			h.PortHost = "[" + h.PortHost + "]"
		}
		out = append(out, h)
	}
	return out, nil
}
