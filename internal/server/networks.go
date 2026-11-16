package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"strings"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// netSpec is a network as the form describes it.
type netSpec struct {
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Parent   string `json:"parent"` // host interface, macvlan and ipvlan only
	Subnet   string `json:"subnet"`
	Gateway  string `json:"gateway"`
	IPv6     bool   `json:"ipv6"`
	Subnet6  string `json:"subnet6"`
	Internal bool   `json:"internal"`
}

var netNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

// predefined networks belong to Docker itself.
var predefined = map[string]bool{"bridge": true, "host": true, "none": true}

// options checks the form and turns it into Docker's create options.
func (n netSpec) options() (client.NetworkCreateOptions, error) {
	var o client.NetworkCreateOptions
	if !netNameRe.MatchString(n.Name) {
		return o, errors.New("nom invalide : lettres, chiffres, - _ et .")
	}
	if predefined[n.Name] {
		return o, fmt.Errorf("%s est un réseau de Docker", n.Name)
	}
	switch n.Driver {
	case "", "bridge":
		o.Driver = "bridge"
	case "macvlan", "ipvlan":
		o.Driver = n.Driver
		if strings.TrimSpace(n.Parent) == "" {
			return o, fmt.Errorf("%s demande l'interface de l'hôte (ex. eth0)", n.Driver)
		}
		o.Options = map[string]string{"parent": strings.TrimSpace(n.Parent)}
	default:
		return o, fmt.Errorf("pilote %q non pris en charge", n.Driver)
	}
	o.Internal = n.Internal
	ipam := &network.IPAM{}
	if s := strings.TrimSpace(n.Subnet); s != "" {
		p, err := netip.ParsePrefix(s)
		if err != nil || !p.Addr().Is4() {
			return o, fmt.Errorf("sous-réseau IPv4 invalide : %s", s)
		}
		cfg := network.IPAMConfig{Subnet: p.Masked()}
		if g := strings.TrimSpace(n.Gateway); g != "" {
			a, err := netip.ParseAddr(g)
			if err != nil || !p.Masked().Contains(a) {
				return o, fmt.Errorf("la passerelle %s n'est pas dans %s", g, p.Masked())
			}
			cfg.Gateway = a
		}
		ipam.Config = append(ipam.Config, cfg)
	} else if strings.TrimSpace(n.Gateway) != "" {
		return o, errors.New("une passerelle demande un sous-réseau")
	}
	// Always explicit: unset, the daemon's default (possibly IPv6) applies.
	ipv6 := n.IPv6
	o.EnableIPv6 = &ipv6
	if n.IPv6 {
		if s := strings.TrimSpace(n.Subnet6); s != "" {
			p, err := netip.ParsePrefix(s)
			if err != nil || !p.Addr().Is6() {
				return o, fmt.Errorf("sous-réseau IPv6 invalide : %s", s)
			}
			ipam.Config = append(ipam.Config, network.IPAMConfig{Subnet: p.Masked()})
		}
	}
	if len(ipam.Config) > 0 {
		o.IPAM = ipam
	}
	return o, nil
}

// netWrite gates the network routes: advanced mode, and a proxy that lets
// dockerr change networks.
func (s *Server) netWrite(w http.ResponseWriter, r *http.Request) bool {
	if !s.advancedOnly(w, r) {
		return false
	}
	if !s.docker.Caps.Networks || !s.docker.Caps.Write {
		jsonError(w, http.StatusForbidden, "le proxy Docker refuse de modifier les réseaux (NETWORKS=1 et POST=1)")
		return false
	}
	return true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		jsonError(w, http.StatusBadRequest, "requête invalide")
		return false
	}
	return true
}

func (s *Server) networkCreate(w http.ResponseWriter, r *http.Request) {
	var n netSpec
	if !s.netWrite(w, r) || !decode(w, r, &n) {
		return
	}
	o, err := n.options()
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.docker.API.NetworkCreate(r.Context(), n.Name, o); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": n.Name})
}

// editable inspects a network that dockerr may remove or recreate: not one
// of Docker's, not one a compose file declares, with no container on it.
func (s *Server) editable(ctx context.Context, id string) (network.Inspect, error) {
	res, err := s.docker.API.NetworkInspect(ctx, id, client.NetworkInspectOptions{})
	if err != nil {
		return network.Inspect{}, err
	}
	n := res.Network
	switch {
	case predefined[n.Name]:
		return n, fmt.Errorf("%s est un réseau de Docker", n.Name)
	case n.Labels[api.ProjectLabel] != "":
		return n, fmt.Errorf("%s appartient à la pile %s : modifie-le dans son compose", n.Name, n.Labels[api.ProjectLabel])
	case len(n.Containers) > 0:
		return n, fmt.Errorf("%s est utilisé par %d conteneur(s) : déconnecte-les d'abord", n.Name, len(n.Containers))
	}
	return n, nil
}

func (s *Server) networkRemove(w http.ResponseWriter, r *http.Request) {
	if !s.netWrite(w, r) {
		return
	}
	n, err := s.editable(r.Context(), r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusConflict, err.Error())
		return
	}
	if _, err := s.docker.API.NetworkRemove(r.Context(), n.ID, client.NetworkRemoveOptions{}); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// networkRecreate changes a network: Docker cannot edit one, so it is
// removed and created again, and put back as it was if that fails.
func (s *Server) networkRecreate(w http.ResponseWriter, r *http.Request) {
	var spec netSpec
	if !s.netWrite(w, r) || !decode(w, r, &spec) {
		return
	}
	o, err := spec.options()
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	old, err := s.editable(ctx, r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusConflict, err.Error())
		return
	}
	if _, err := s.docker.API.NetworkRemove(ctx, old.ID, client.NetworkRemoveOptions{}); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	if _, err := s.docker.API.NetworkCreate(ctx, spec.Name, o); err != nil {
		back := client.NetworkCreateOptions{Driver: old.Driver, Internal: old.Internal, Attachable: old.Attachable,
			EnableIPv6: &old.EnableIPv6, IPAM: &old.IPAM, Options: old.Options, Labels: old.Labels}
		if _, rerr := s.docker.API.NetworkCreate(ctx, old.Name, back); rerr != nil {
			jsonError(w, http.StatusBadGateway, fmt.Sprintf("%v ; l'ancien réseau n'a pas pu être remis : %v", err, rerr))
			return
		}
		jsonError(w, http.StatusBadGateway, err.Error()+" ; l'ancien réseau est remis")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": spec.Name})
}

type netAttach struct {
	Container string   `json:"container"`
	IPv4      string   `json:"ipv4"`
	Aliases   []string `json:"aliases"`
}

func (s *Server) networkConnect(w http.ResponseWriter, r *http.Request) {
	var a netAttach
	if !s.netWrite(w, r) || !decode(w, r, &a) {
		return
	}
	if a.Container == "" {
		jsonError(w, http.StatusBadRequest, "conteneur manquant")
		return
	}
	ep := &network.EndpointSettings{}
	for _, al := range a.Aliases {
		if al = strings.TrimSpace(al); al != "" {
			ep.Aliases = append(ep.Aliases, al)
		}
	}
	if ip := strings.TrimSpace(a.IPv4); ip != "" {
		addr, err := netip.ParseAddr(ip)
		if err != nil || !addr.Is4() {
			jsonError(w, http.StatusBadRequest, "adresse IPv4 invalide : "+ip)
			return
		}
		ep.IPAMConfig = &network.EndpointIPAMConfig{IPv4Address: addr}
	}
	if _, err := s.docker.API.NetworkConnect(r.Context(), r.PathValue("id"), client.NetworkConnectOptions{Container: a.Container, EndpointConfig: ep}); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) networkDisconnect(w http.ResponseWriter, r *http.Request) {
	var a netAttach
	if !s.netWrite(w, r) || !decode(w, r, &a) {
		return
	}
	if a.Container == "" {
		jsonError(w, http.StatusBadRequest, "conteneur manquant")
		return
	}
	if _, err := s.docker.API.NetworkDisconnect(r.Context(), r.PathValue("id"), client.NetworkDisconnectOptions{Container: a.Container}); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
