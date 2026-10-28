// Package config reads dockerr's settings from the environment.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Listen    string
	DataDir   string
	StacksDir string

	// AuthHeader carries the user name set by the forward-auth proxy
	// (Authelia: Remote-User). It is only trusted from TrustedProxies.
	AuthHeader     string
	TrustedProxies []netip.Prefix
	TrustedHosts   []string
	// RequireGroup, when set, must appear in the GroupsHeader list.
	RequireGroup string
	GroupsHeader string
	// InsecureNoAuth skips the header check; meant for a local bench only.
	InsecureNoAuth bool

	// ExecEnabled turns on the container terminal. The proxy must allow EXEC too.
	ExecEnabled bool
	// DozzleURL, when set, adds a logs link per container.
	DozzleURL string
	// APIKeysFile lists SHA-256 hashes (hex, one per line) of the keys that
	// may read /api/status. Empty: the endpoint is off.
	APIKeysFile string
	// PortHost is the host used in links to published ports; by default the
	// host dockerr is reached on.
	PortHost string

	UpdateInterval time.Duration
	// RepairNetDeps recreates containers left without a network namespace
	// after an operation (network_mode: container:/service:).
	RepairNetDeps bool
}

func Load() (*Config, error) {
	c := &Config{
		Listen:         env("DOCKERR_LISTEN", ":8080"),
		DataDir:        env("DOCKERR_DATA_DIR", "./data"),
		StacksDir:      env("DOCKERR_STACKS_DIR", "/mnt/docker"),
		AuthHeader:     env("DOCKERR_AUTH_HEADER", "Remote-User"),
		InsecureNoAuth: envBool("DOCKERR_INSECURE_NOAUTH", false),
		RequireGroup:   os.Getenv("DOCKERR_REQUIRE_GROUP"),
		GroupsHeader:   env("DOCKERR_GROUPS_HEADER", "Remote-Groups"),
		ExecEnabled:    envBool("DOCKERR_EXEC", false),
		DozzleURL:      strings.TrimRight(os.Getenv("DOCKERR_DOZZLE_URL"), "/"),
		PortHost:       os.Getenv("DOCKERR_PORT_HOST"),
		APIKeysFile:    os.Getenv("DOCKERR_API_KEYS_FILE"),
		RepairNetDeps:  envBool("DOCKERR_REPAIR_NETDEPS", true),
	}
	var err error
	if c.UpdateInterval, err = time.ParseDuration(env("DOCKERR_UPDATE_INTERVAL", "6h")); err != nil {
		return nil, fmt.Errorf("DOCKERR_UPDATE_INTERVAL: %w", err)
	}
	// Who may set the auth header: IPs, CIDRs, or host names resolved on the
	// fly (e.g. "traefik" on a Docker network). No default: anything else on
	// the network could otherwise claim to be any user.
	for _, s := range strings.FieldsFunc(os.Getenv("DOCKERR_TRUSTED_PROXIES"), func(r rune) bool { return r == ',' || r == ' ' }) {
		if p, err := netip.ParsePrefix(s); err == nil {
			c.TrustedProxies = append(c.TrustedProxies, p.Masked())
		} else if a, err := netip.ParseAddr(s); err == nil {
			c.TrustedProxies = append(c.TrustedProxies, netip.PrefixFrom(a, a.BitLen()))
		} else {
			c.TrustedHosts = append(c.TrustedHosts, s)
		}
	}
	if !c.InsecureNoAuth && len(c.TrustedProxies) == 0 && len(c.TrustedHosts) == 0 {
		return nil, fmt.Errorf("DOCKERR_TRUSTED_PROXIES est requis : l'adresse ou le nom du proxy d'authentification (ex. traefik)")
	}
	return c, nil
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	v, err := strconv.ParseBool(os.Getenv(k))
	if err != nil {
		return def
	}
	return v
}
