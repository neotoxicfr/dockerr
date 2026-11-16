package server

import (
	"strings"
	"testing"
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
		{"gateway outside", netSpec{Name: "lan", Subnet: "172.30.0.0/24", Gateway: "10.0.0.1"}, "n'est pas dans", nil},
		{"gateway without subnet", netSpec{Name: "lan", Gateway: "10.0.0.1"}, "demande un sous-réseau", nil},
		{"ipv6 subnet", netSpec{Name: "v6", IPv6: true, Subnet6: "fd00:1::/64"}, "", nil},
		{"ipv4 in ipv6 field", netSpec{Name: "v6", IPv6: true, Subnet6: "10.0.0.0/8"}, "IPv6 invalide", nil},
		{"macvlan needs parent", netSpec{Name: "mv", Driver: "macvlan"}, "interface de l'hôte", nil},
		{"macvlan", netSpec{Name: "mv", Driver: "macvlan", Parent: "eth0", Subnet: "192.168.1.0/24"}, "", nil},
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
