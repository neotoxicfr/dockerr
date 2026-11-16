package server

import (
	"strings"
	"testing"

	"github.com/moby/moby/api/types/network"
)

func TestNetSpecOptions(t *testing.T) {
	cases := []struct {
		name    string
		spec    netSpec
		wantErr string
		check   func(t *testing.T, s netSpec)
	}{
		{"plain bridge", netSpec{Name: "front"}, "", nil},
		{"subnet and gateway", netSpec{Name: "lan", Subnet: "172.30.0.0/24", Gateway: "172.30.0.1"}, "", nil},
		{"subnet is masked", netSpec{Name: "lan", Subnet: "172.30.0.7/24"}, "", nil},
		{"gateway outside", netSpec{Name: "lan", Subnet: "172.30.0.0/24", Gateway: "192.0.2.1"}, "n'est pas dans", nil},
		{"gateway without subnet", netSpec{Name: "lan", Gateway: "192.0.2.1"}, "demande un sous-réseau", nil},
		{"ipv6 subnet", netSpec{Name: "v6", IPv6: true, Subnet6: "fd00:1::/64"}, "", nil},
		{"ipv4 in ipv6 field", netSpec{Name: "v6", IPv6: true, Subnet6: "192.0.2.0/24"}, "IPv6 invalide", nil},
		{"macvlan needs parent", netSpec{Name: "mv", Driver: "macvlan"}, "interface de l'hôte", nil},
		{"macvlan", netSpec{Name: "mv", Driver: "macvlan", Parent: "eth0", Subnet: "198.51.100.0/24"}, "", nil},
		{"overlay refused", netSpec{Name: "ov", Driver: "overlay"}, "non pris en charge", nil},
		{"bad name", netSpec{Name: "a b"}, "nom invalide", nil},
		{"docker's own", netSpec{Name: "host"}, "réseau de Docker", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := c.spec.options()
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.spec.Subnet != "" && (o.IPAM == nil || o.IPAM.Config[0].Subnet.String() != "172.30.0.0/24" && c.spec.Driver == "") {
				t.Errorf("IPAM = %+v", o.IPAM)
			}
			if c.spec.Driver == "macvlan" && o.Options["parent"] != "eth0" {
				t.Errorf("options = %v", o.Options)
			}
		})
	}
}

func TestKeepUnshown(t *testing.T) {
	old := network.Inspect{Network: network.Network{
		Labels: map[string]string{"team": "a"}, Attachable: true,
		Options: map[string]string{"com.docker.network.driver.mtu": "1400", "parent": "eth1"},
		IPAM:    network.IPAM{Driver: "default"},
	}}
	o, err := netSpec{Name: "lan", Driver: "macvlan", Parent: "eth0"}.options()
	if err != nil {
		t.Fatal(err)
	}
	keepUnshown(&o, old)
	if o.Labels["team"] != "a" || !o.Attachable || o.Options["com.docker.network.driver.mtu"] != "1400" {
		t.Errorf("lost settings: %+v", o)
	}
	if o.Options["parent"] != "eth0" {
		t.Errorf("parent = %q, the form's value must win", o.Options["parent"])
	}
	if o.IPAM != nil {
		t.Errorf("default IPAM driver must not create an IPAM block: %+v", o.IPAM)
	}
}
