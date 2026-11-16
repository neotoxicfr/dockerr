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

	// NtfyURL (https://server/topic), NtfyToken and WebhookURL turn on
	// notifications; empty: off.
	NtfyURL, NtfyToken, WebhookURL string

	UpdateInterval time.Duration
	// RepairNetDeps recreates containers left without a network namespace
	// after an operation (network_mode: container:/service:).
	RepairNetDeps bool
	// AutoUpdateHour and AutoUpdateMinute are the daily time (hour, minute; container local time)
	// when opted-in stacks are updated.
	AutoUpdateHour, AutoUpdateMinute int

	// HostName names the Docker host of DOCKER_HOST and StacksDir.
	HostName string
	// Hosts are the other Docker hosts, from DOCKERR_HOSTS.
	Hosts []Host
}

// ParseClock reads "HH:MM" (24 h).
func ParseClock(s string) (hour, minute int, err error) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if hour, err = strconv.Atoi(h); !ok || err != nil || len(h) != 2 || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("%q n'est pas une heure HH:MM", s)
	}
	if minute, err = strconv.Atoi(m); err != nil || len(m) != 2 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("%q n'est pas une heure HH:MM", s)
	}
	return hour, minute, nil
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
		NtfyURL:        os.Getenv("DOCKERR_NTFY_URL"),
		NtfyToken:      os.Getenv("DOCKERR_NTFY_TOKEN"),
		WebhookURL:     os.Getenv("DOCKERR_WEBHOOK_URL"),
		HostName:       env("DOCKERR_HOST_NAME", "local"),
	}
	var err error
	// A token file keeps the secret out of the container's environment.
	if f := os.Getenv("DOCKERR_NTFY_TOKEN_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("DOCKERR_NTFY_TOKEN_FILE: %w", err)
		}
		c.NtfyToken = strings.TrimSpace(string(b))
	}
	if c.UpdateInterval, err = time.ParseDuration(env("DOCKERR_UPDATE_INTERVAL", "6h")); err != nil {
		return nil, fmt.Errorf("DOCKERR_UPDATE_INTERVAL: %w", err)
	}
	if c.AutoUpdateHour, c.AutoUpdateMinute, err = ParseClock(env("DOCKERR_AUTOUPDATE_AT", "04:00")); err != nil {
		return nil, fmt.Errorf("DOCKERR_AUTOUPDATE_AT: %w", err)
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
	if !hostNameRe.MatchString(c.HostName) {
		return nil, fmt.Errorf("DOCKERR_HOST_NAME %q : lettres minuscules, chiffres, - et _", c.HostName)
	}
	if c.Hosts, err = parseHosts(os.Getenv, c.HostName); err != nil {
		return nil, err
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
