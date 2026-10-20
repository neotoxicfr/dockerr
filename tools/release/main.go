// Command release builds and publishes a dockerr release from this machine,
// without GitHub Actions: checks, cross-compiled binaries, a multi-arch image
// on distroless pushed to GHCR, the git tag, and the GitHub release with
// hand-written notes.
//
//	go run ./tools/release -version v0.1.1 -notes /path/to/notes.md
//
// Run it from the repository root, on a clean, pushed dev branch. -dry builds
// everything and pushes nothing.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

var ctx = context.Background()

const (
	repo   = "ghcr.io/neotoxicfr/dockerr"
	ghRepo = "neotoxicfr/dockerr"
	pkg    = "./cmd/dockerr"
	ldVar  = "github.com/neotoxicfr/dockerr/internal/server.Version"
)

var platforms = []v1.Platform{
	{OS: "linux", Architecture: "amd64"},
	{OS: "linux", Architecture: "arm64"},
	{OS: "linux", Architecture: "arm", Variant: "v7"},
}

var semver = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

func main() {
	version := flag.String("version", "", "release tag, vX.Y.Z")
	notes := flag.String("notes", "", "release notes file (Markdown, written by hand)")
	dry := flag.Bool("dry", false, "build everything, push nothing")
	skipChecks := flag.Bool("skip-checks", false, "skip make check (only after running it yourself)")
	flag.Parse()
	log.SetFlags(0)

	if !semver.MatchString(*version) || *notes == "" {
		log.Fatal("usage: release -version vX.Y.Z -notes notes.md [-dry]")
	}
	imageTag := strings.TrimPrefix(*version, "v") // image tags carry no "v"
	notesText, err := os.ReadFile(*notes)
	must(err)
	if strings.Contains(string(notesText), ":v"+imageTag) {
		log.Fatalf("notes mention :v%s: image tags carry no v", imageTag)
	}

	preflight(*version)
	if !*skipChecks {
		step("make check")
		run("make", "check")
	}
	revision := out("git", "rev-parse", "HEAD")
	created, err := time.Parse(time.RFC3339, out("git", "show", "-s", "--format=%cI", "HEAD"))
	must(err)
	log.Printf("%s at %s, %s", *version, revision[:7], out("go", "version"))

	dist := filepath.Join("bin", "dist", *version)
	must(os.RemoveAll(dist))
	must(os.MkdirAll(dist, 0o755))

	base := baseImage()
	idx := mutate.IndexMediaType(empty.Index, types.OCIImageIndex)
	var sums bytes.Buffer
	for _, p := range platforms {
		bin := build(dist, p, imageTag)
		archive := pack(dist, bin, p, *version)
		sums.WriteString(sha(archive) + "  " + filepath.Base(archive) + "\n")
		img := image(base, p, bin, imageTag, revision, created)
		d, err := img.Digest()
		must(err)
		log.Printf("  %s image %s", platformString(p), d.String()[:19])
		pl := p
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &pl}})
	}
	sumsFile := filepath.Join(dist, "SHA256SUMS")
	must(os.WriteFile(sumsFile, sums.Bytes(), 0o644))

	if *dry {
		log.Printf("dry run: artifacts in %s, nothing pushed", dist)
		return
	}

	auth := ghcrAuth()
	ref, err := name.NewTag(repo + ":" + imageTag)
	must(err)
	step("push " + ref.String())
	must(remote.WriteIndex(ref, idx, remote.WithAuth(auth)))
	latest, err := name.NewTag(repo + ":latest")
	must(err)
	must(remote.Tag(latest, idx, remote.WithAuth(auth)))
	verify(ref)

	step("tag and release")
	run("git", "tag", "-a", *version, "-m", *version)
	run("git", "push", "origin", *version)
	assets := []string{sumsFile}
	for _, p := range platforms {
		assets = append(assets, archiveName(dist, p, *version))
	}
	run("gh", append([]string{"release", "create", *version, "-R", ghRepo, "--verify-tag", "--latest",
		"--title", *version, "--notes-file", *notes}, assets...)...)
	// main follows the releases.
	run("git", "push", "origin", "HEAD:main")
	log.Printf("done: %s, docker pull %s", *version, ref.String())
}

// preflight refuses a release from a dirty, unpushed or already-tagged tree.
func preflight(version string) {
	if s := out("git", "status", "--porcelain"); s != "" {
		log.Fatalf("working tree not clean:\n%s", s)
	}
	if b := out("git", "rev-parse", "--abbrev-ref", "HEAD"); b != "dev" {
		log.Fatalf("release from dev, not %s", b)
	}
	run("git", "fetch", "-q", "origin", "dev", "--tags")
	if out("git", "rev-parse", "HEAD") != out("git", "rev-parse", "origin/dev") {
		log.Fatal("dev is not pushed (or behind origin/dev)")
	}
	if exec.CommandContext(ctx, "git", "rev-parse", "-q", "--verify", "refs/tags/"+version).Run() == nil {
		log.Fatalf("tag %s already exists", version)
	}
}

// baseImage reads the runtime base from the Dockerfile, so both stay pinned
// to the same digest.
func baseImage() v1.ImageIndex {
	b, err := os.ReadFile("Dockerfile")
	must(err)
	m := regexp.MustCompile(`(?m)^FROM (gcr\.io/distroless/\S+@sha256:[0-9a-f]{64})\s*$`).FindSubmatch(b)
	if m == nil {
		log.Fatal("no pinned distroless FROM line in the Dockerfile")
	}
	ref, err := name.ParseReference(string(m[1]))
	must(err)
	idx, err := remote.Index(ref)
	must(err)
	log.Printf("base %s", ref)
	return idx
}

func build(dist string, p v1.Platform, version string) string {
	bin := filepath.Join(dist, "dockerr-"+suffix(p))
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-s -w -X "+ldVar+"="+version, "-o", bin, pkg)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+p.OS, "GOARCH="+p.Architecture)
	if p.Variant != "" {
		cmd.Env = append(cmd.Env, "GOARM="+strings.TrimPrefix(p.Variant, "v"))
	}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	must(cmd.Run())
	return bin
}

func image(base v1.ImageIndex, p v1.Platform, bin, version, revision string, created time.Time) v1.Image {
	im, err := base.IndexManifest()
	must(err)
	var img v1.Image
	for _, d := range im.Manifests {
		if d.Platform != nil && d.Platform.OS == p.OS && d.Platform.Architecture == p.Architecture && sameVariant(d.Platform.Variant, p.Variant) {
			img, err = base.Image(d.Digest)
			must(err)
		}
	}
	if img == nil {
		log.Fatalf("base image has no %s", platformString(p))
	}
	layer := appLayer(bin, created)
	img, err = mutate.Append(img, mutate.Addendum{Layer: layer, History: v1.History{
		Created: v1.Time{Time: created}, CreatedBy: "tools/release: /dockerr, /data", Comment: "dockerr " + version,
	}})
	must(err)
	cf, err := img.ConfigFile()
	must(err)
	c := cf.Config
	c.Entrypoint = []string{"/dockerr"}
	c.Cmd = nil
	c.User = "nonroot"
	c.Env = append(c.Env, "DOCKERR_LISTEN=:8080", "DOCKERR_DATA_DIR=/data")
	c.ExposedPorts = map[string]struct{}{"8080/tcp": {}}
	c.Volumes = map[string]struct{}{"/data": {}}
	c.Healthcheck = &v1.HealthConfig{
		Test: []string{"CMD", "/dockerr", "healthcheck"}, Interval: 30 * time.Second, Timeout: 5 * time.Second, StartPeriod: 10 * time.Second,
	}
	c.Labels = map[string]string{
		"org.opencontainers.image.title":       "dockerr",
		"org.opencontainers.image.description": "A small, fast web manager for Docker compose stacks",
		"org.opencontainers.image.source":      "https://github.com/" + ghRepo,
		"org.opencontainers.image.url":         "https://github.com/" + ghRepo,
		"org.opencontainers.image.licenses":    "MIT",
		"org.opencontainers.image.version":     version,
		"org.opencontainers.image.revision":    revision,
		"org.opencontainers.image.created":     created.UTC().Format(time.RFC3339),
	}
	img, err = mutate.Config(img, c)
	must(err)
	img, err = mutate.CreatedAt(img, v1.Time{Time: created})
	must(err)
	return mutate.Annotations(img, map[string]string{
		"org.opencontainers.image.source":   "https://github.com/" + ghRepo,
		"org.opencontainers.image.revision": revision,
		"org.opencontainers.image.version":  version,
	}).(v1.Image)
}

// appLayer holds the binary and /data owned by nonroot, so a fresh named
// volume is writable.
func appLayer(bin string, created time.Time) v1.Layer {
	data, err := os.ReadFile(bin)
	must(err)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	must(tw.WriteHeader(&tar.Header{Name: "dockerr", Mode: 0o755, Size: int64(len(data)), ModTime: created, Typeflag: tar.TypeReg, Format: tar.FormatPAX}))
	_, err = tw.Write(data)
	must(err)
	must(tw.WriteHeader(&tar.Header{Name: "data/", Mode: 0o755, Uid: 65532, Gid: 65532, ModTime: created, Typeflag: tar.TypeDir, Format: tar.FormatPAX}))
	must(tw.Close())
	b := buf.Bytes()
	l, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil },
		tarball.WithMediaType(types.OCILayer))
	must(err)
	return l
}

func pack(dist, bin string, p v1.Platform, version string) string {
	path := archiveName(dist, p, version)
	f, err := os.Create(path)
	must(err)
	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	data, err := os.ReadFile(bin)
	must(err)
	must(tw.WriteHeader(&tar.Header{Name: "dockerr", Mode: 0o755, Size: int64(len(data)), ModTime: time.Now()}))
	_, err = tw.Write(data)
	must(err)
	for _, extra := range []string{"README.md", "LICENSE"} {
		x, err := os.ReadFile(extra)
		must(err)
		must(tw.WriteHeader(&tar.Header{Name: extra, Mode: 0o644, Size: int64(len(x)), ModTime: time.Now()}))
		_, err = tw.Write(x)
		must(err)
	}
	must(tw.Close())
	must(zw.Close())
	must(f.Close())
	return path
}

func archiveName(dist string, p v1.Platform, version string) string {
	return filepath.Join(dist, fmt.Sprintf("dockerr_%s_%s.tar.gz", strings.TrimPrefix(version, "v"), suffix(p)))
}

// verify pulls the index back and checks every platform is there.
func verify(ref name.Tag) {
	idx, err := remote.Index(ref)
	must(err)
	im, err := idx.IndexManifest()
	must(err)
	if len(im.Manifests) != len(platforms) {
		log.Fatalf("pushed index has %d manifests, want %d", len(im.Manifests), len(platforms))
	}
	d, _ := idx.Digest()
	log.Printf("  %s: %d platforms, %s", ref, len(im.Manifests), d)
}

func ghcrAuth() authn.Authenticator {
	user := out("gh", "api", "user", "-q", ".login")
	return &authn.Basic{Username: user, Password: out("gh", "auth", "token")}
}

// sameVariant treats arm64 "v8" and no variant as the same.
func sameVariant(a, b string) bool {
	norm := func(v string) string {
		if v == "v8" {
			return ""
		}
		return v
	}
	return norm(a) == norm(b)
}

func suffix(p v1.Platform) string {
	if p.Variant != "" {
		return p.OS + "_" + p.Architecture + p.Variant
	}
	return p.OS + "_" + p.Architecture
}

func platformString(p v1.Platform) string {
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

func sha(path string) string {
	f, err := os.Open(path)
	must(err)
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	must(err)
	return hex.EncodeToString(h.Sum(nil))
}

func step(s string) { log.Printf("== %s", s) }

func run(name string, args ...string) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("%s %s: %v", name, strings.Join(args, " "), err)
	}
}

func out(name string, args ...string) string {
	b, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			log.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, ee.Stderr)
		}
		log.Fatalf("%s: %v", name, err)
	}
	return strings.TrimSpace(string(b))
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
