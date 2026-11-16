package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseHosts(t *testing.T) {
	certs := writeCerts(t)
	junk := t.TempDir() // the three files, unreadable
	for _, f := range TLSFiles {
		if err := os.WriteFile(filepath.Join(junk, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	partial := t.TempDir()
	if err := os.WriteFile(filepath.Join(partial, "ca.pem"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		env  map[string]string
		want []Host
		err  string
	}{
		{name: "none", env: map[string]string{}},
		{name: "plain and TLS", env: map[string]string{
			"DOCKERR_HOSTS":            "pi=tcp://pi.example:2376, nas-2=tcp://192.0.2.10:2375",
			"DOCKERR_TLS_PI_DIR":       certs,
			"DOCKERR_STACKS_DIR_NAS_2": "/mnt/nas2",
			"DOCKERR_PORT_HOST_NAS_2":  "nas2.example",
		}, want: []Host{
			{Name: "pi", URL: "tcp://pi.example:2376", TLSDir: certs, PortHost: "pi.example"},
			{Name: "nas-2", URL: "tcp://192.0.2.10:2375", StacksDir: "/mnt/nas2", PortHost: "nas2.example"},
		}},
		{name: "DOCKER_CERT_PATH by default", env: map[string]string{
			"DOCKERR_HOSTS": "pi=tcp://pi.example:2376/", "DOCKER_CERT_PATH": certs,
		}, want: []Host{{Name: "pi", URL: "tcp://pi.example:2376", TLSDir: certs, PortHost: "pi.example"}}},
		{name: "IPv6", env: map[string]string{"DOCKERR_HOSTS": "v6=tcp://[2001:db8::1]:2376"},
			want: []Host{{Name: "v6", URL: "tcp://[2001:db8::1]:2376", PortHost: "[2001:db8::1]"}}},
		{name: "unix refused", env: map[string]string{"DOCKERR_HOSTS": "pi=unix:///var/run/docker.sock"}, err: "invalide"},
		{name: "http refused", env: map[string]string{"DOCKERR_HOSTS": "pi=http://pi.example:2375"}, err: "invalide"},
		{name: "port required", env: map[string]string{"DOCKERR_HOSTS": "pi=tcp://pi.example"}, err: "invalide"},
		{name: "no host", env: map[string]string{"DOCKERR_HOSTS": "pi=tcp://:2375"}, err: "invalide"},
		{name: "path refused", env: map[string]string{"DOCKERR_HOSTS": "pi=tcp://pi.example:2375/x"}, err: "invalide"},
		{name: "user refused", env: map[string]string{"DOCKERR_HOSTS": "pi=tcp://u:p@pi.example:2375"}, err: "invalide"},
		{name: "garbage", env: map[string]string{"DOCKERR_HOSTS": "pi=tcp://pi example:2375"}, err: "invalide"},
		{name: "no name", env: map[string]string{"DOCKERR_HOSTS": "tcp://pi.example:2375"}, err: "nom=tcp"},
		{name: "bad name", env: map[string]string{"DOCKERR_HOSTS": "Pi=tcp://pi.example:2375"}, err: "nom=tcp"},
		{name: "duplicate", env: map[string]string{"DOCKERR_HOSTS": "pi=tcp://a.example:1,pi=tcp://b.example:2"}, err: "déjà pris"},
		{name: "local name taken", env: map[string]string{"DOCKERR_HOSTS": "local=tcp://a.example:1"}, err: "déjà pris"},
		{name: "unreadable TLS files", env: map[string]string{"DOCKERR_HOSTS": "pi=tcp://a.example:1", "DOCKERR_TLS_PI_DIR": junk}, err: "illisible"},
		{name: "same variables", env: map[string]string{"DOCKERR_HOSTS": "nas-2=tcp://a.example:1,nas_2=tcp://b.example:2"}, err: "mêmes variables"},
		{name: "missing TLS file", env: map[string]string{"DOCKERR_HOSTS": "pi=tcp://a.example:1", "DOCKERR_TLS_PI_DIR": partial}, err: "cert.pem"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseHosts(func(k string) string { return tt.env[k] }, "local")
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

// writeCerts writes a self-signed ca.pem, cert.pem and key.pem.
func writeCerts(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	files := map[string][]byte{"ca.pem": cert, "cert.pem": cert,
		"key.pem": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
