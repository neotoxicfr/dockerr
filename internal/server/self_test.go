package server

import (
	"strings"
	"testing"
)

func TestContainerIDFromMountinfo(t *testing.T) {
	id := strings.Repeat("ab12", 16)
	cases := []struct {
		name, in, want string
	}{
		{"docker hostname mount", "1234 1200 8:1 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw,relatime - ext4 /dev/sda1 rw", id},
		{"custom data root", "88 70 0:45 /data/containers/" + id + "/hostname /etc/hostname rw - xfs /dev/x rw", id},
		{"another container's folder bound first", "77 70 8:1 /srv/containers/" + strings.Repeat("cd34", 16) + "/data /data rw - ext4 /dev/sda1 rw\n" +
			"78 70 8:1 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw - ext4 /dev/sda1 rw", id},
		{"only resolv.conf", "88 70 0:45 /containers/" + id + "/resolv.conf /etc/resolv.conf rw - xfs /dev/x rw", ""},
		{"not a container", "22 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw", ""},
		{"short id ignored", "5 1 8:1 /containers/abc123/hostname /etc/hostname rw - ext4 /dev/sda1 rw", ""},
	}
	for _, c := range cases {
		if got := containerIDFromMountinfo(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
