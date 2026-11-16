package server

import (
	"archive/tar"
	"bytes"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"
)

func TestCleanPath(t *testing.T) {
	for in, want := range map[string]string{
		"":              "/",
		"/":             "/",
		"etc":           "/etc",
		"/etc/../..":    "/",
		"../../etc":     "/etc",
		"/a/./b//c/":    "/a/b/c",
		"/a/b/../../..": "/",
	} {
		if got, ok := cleanPath(in); !ok || got != want {
			t.Errorf("cleanPath(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := cleanPath("/etc/\x00passwd"); ok {
		t.Error("NUL accepted")
	}
	// A volume path never leaves the mount.
	vt := &fileTarget{root: volRoot}
	for _, in := range []string{"/", "..", "/../../etc/passwd", "a/../../.."} {
		p, _ := cleanPath(in)
		if got := vt.real(p); got != volRoot && !strings.HasPrefix(got, volRoot+"/") {
			t.Errorf("real(%q) = %q, outside %s", in, got, volRoot)
		}
	}
}

func TestValidFileName(t *testing.T) {
	for n, want := range map[string]bool{
		"a.txt": true, "été.conf": true, ".env": true,
		"": false, ".": false, "..": false, "a/b": false, "../x": false, `a\b`: false, "a\x00": false,
		strings.Repeat("x", 256): false,
	} {
		if got := validFileName(n); got != want {
			t.Errorf("validFileName(%q) = %v", n, got)
		}
	}
}

type ent struct {
	name string
	typ  byte
	size int
	link string
}

func archive(t *testing.T, ents []ent) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, e := range ents {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644, Size: int64(e.size), Linkname: e.link, ModTime: time.Unix(1e9, 0)}
		if e.typ == tar.TypeDir {
			h.Mode = 0o755
		}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			_, _ = tw.Write(bytes.Repeat([]byte("x"), e.size))
		}
	}
	_ = tw.Close()
	return b.Bytes()
}

func names(l dirList) string {
	var out []string
	for _, e := range l.Entries {
		n := e.Name
		if e.Dir {
			n += "/"
		}
		if e.Link != "" {
			n += "->" + e.Link
		}
		out = append(out, n)
	}
	return strings.Join(out, " ")
}

func TestReadDir(t *testing.T) {
	etc := []ent{
		{"etc/", tar.TypeDir, 0, ""},
		{"etc/b.conf", tar.TypeReg, 10, ""},
		{"etc/ssl/", tar.TypeDir, 0, ""},
		{"etc/ssl/certs/", tar.TypeDir, 0, ""},
		{"etc/ssl/certs/ca.pem", tar.TypeReg, 2000, ""},
		{"etc/a", tar.TypeReg, 1, ""},
		{"etc/mtab", tar.TypeSymlink, 0, "/proc/mounts"},
		{"etc/z/deep/f", tar.TypeReg, 1, ""}, // folder without its own entry
	}
	root := []ent{
		{"/", tar.TypeDir, 0, ""},
		{"/.dockerenv", tar.TypeReg, 0, ""},
		{"/bin/", tar.TypeDir, 0, ""},
		{"/bin/sh", tar.TypeReg, 5, ""},
	}
	tests := []struct {
		name       string
		ents       []ent
		dir        string
		max        int
		maxBytes   int64
		want       string
		truncated  bool
		wantPathOf string
	}{
		{"subdir", etc, "/etc", 100, 1 << 20, "ssl/ z/ a b.conf mtab->/proc/mounts", false, "/etc/ssl"},
		{"root", root, "/", 100, 1 << 20, "bin/ .dockerenv", false, "/bin"},
		{"entry bound", etc, "/etc", 2, 1 << 20, "ssl/ b.conf", true, "/etc/ssl"},
		{"exact bound", root, "/", 2, 1 << 20, "bin/ .dockerenv", false, "/bin"},
		// The 2000-byte certificate goes past the byte bound: what came before stays.
		{"byte bound", etc, "/etc", 100, 4096, "ssl/ b.conf", true, "/etc/ssl"},
		{"empty", []ent{{"empty/", tar.TypeDir, 0, ""}}, "/empty", 100, 1 << 20, "", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := readDir(bytes.NewReader(archive(t, tt.ents)), tt.dir, tt.max, tt.maxBytes)
			if err != nil {
				t.Fatal(err)
			}
			if got := names(l); got != tt.want || l.Truncated != tt.truncated {
				t.Errorf("got %q truncated=%v, want %q truncated=%v", got, l.Truncated, tt.want, tt.truncated)
			}
			if tt.wantPathOf != "" && l.Entries[0].Path != tt.wantPathOf {
				t.Errorf("path %q, want %q", l.Entries[0].Path, tt.wantPathOf)
			}
		})
	}
}

func TestReadDirStopsReading(t *testing.T) {
	// A huge subtree: the reader must not be drained past the bound.
	data := archive(t, []ent{{"d/", tar.TypeDir, 0, ""}, {"d/big", tar.TypeReg, 1 << 20, ""}, {"d/after", tar.TypeReg, 1, ""}})
	cr := &countingReader{r: bytes.NewReader(data)}
	l, err := readDir(cr, "/d", 100, 64<<10)
	if err != nil || !l.Truncated {
		t.Fatalf("err=%v truncated=%v", err, l.Truncated)
	}
	if cr.n > 64<<10 {
		t.Errorf("read %d bytes, bound 64 KiB", cr.n)
	}
}

func TestReplaceHeaderKeepsModeAndOwner(t *testing.T) {
	orig := &tar.Header{Name: "app.conf", Typeflag: tar.TypeReg, Mode: 0o100640 | 0o4000, Uid: 1000, Gid: 33, Uname: "app", Gname: "www-data", Size: 3}
	now := time.Unix(2e9, 0)
	var b bytes.Buffer
	if err := writeTar(&b, []tarFile{{replaceHeader(orig, 5, now), strings.NewReader("hello")}}); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&b)
	h, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(tr)
	if h.Name != "app.conf" || h.Typeflag != tar.TypeReg || h.Mode != 0o4640 || h.Uid != 1000 || h.Gid != 33 ||
		h.Uname != "app" || h.Gname != "www-data" || h.Size != 5 || !h.ModTime.Equal(now) || string(body) != "hello" {
		t.Errorf("header %+v body %q", h, body)
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Errorf("more than one entry: %v", err)
	}
}

func TestWriteTarSeveral(t *testing.T) {
	var b bytes.Buffer
	files := []tarFile{
		{tar.Header{Name: "a", Mode: 0o644, Uid: 7, Gid: 8, Size: 1}, strings.NewReader("1")},
		{tar.Header{Name: "b", Mode: 0o644, Uid: 7, Gid: 8, Size: 2}, strings.NewReader("22")},
	}
	if err := writeTar(&b, files); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&b)
	var got []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		c, _ := io.ReadAll(tr)
		if h.Uid != 7 || h.Gid != 8 || h.Typeflag != tar.TypeReg {
			t.Errorf("%s: %+v", h.Name, h)
		}
		got = append(got, h.Name+"="+string(c))
	}
	if strings.Join(got, ",") != "a=1,b=22" {
		t.Errorf("got %v", got)
	}
	// A body shorter than its header's size is an error, not a short file.
	if err := writeTar(io.Discard, []tarFile{{tar.Header{Name: "c", Size: 5}, strings.NewReader("ab")}}); err == nil {
		t.Error("short body accepted")
	}
}

func TestModeString(t *testing.T) {
	for m, want := range map[fs.FileMode]string{
		0o644:                                     "-rw-r--r--",
		fs.ModeDir | 0o755:                        "drwxr-xr-x",
		fs.ModeDir | fs.ModeSticky | 0o777:        "drwxrwxrwt",
		fs.ModeSymlink | 0o777:                    "lrwxrwxrwx",
		fs.ModeSetuid | 0o755:                     "-rwsr-xr-x",
		fs.ModeSetgid | 0o644:                     "-rw-r-Sr--",
		fs.ModeDevice | fs.ModeCharDevice | 0o666: "crw-rw-rw-",
	} {
		if got := modeString(m); got != want {
			t.Errorf("modeString(%v) = %q, want %q", m, got, want)
		}
	}
}

func TestLinkPath(t *testing.T) {
	tests := []struct {
		p, target string
		volume    bool
		want      string
	}{
		{"/etc/mtab", "/proc/mounts", false, "/proc/mounts"},
		{"/etc/mtab", "/proc/mounts", true, ""},
		{"/a/link", "../b", true, "/b"},
		{"/a/link", "../../../../etc", true, "/etc"},
		{"/a/link", "c", false, "/a/c"},
	}
	for _, tt := range tests {
		if got := linkPath(tt.p, tt.target, tt.volume); got != tt.want {
			t.Errorf("linkPath(%q, %q, %v) = %q, want %q", tt.p, tt.target, tt.volume, got, tt.want)
		}
	}
}

func TestCrumbs(t *testing.T) {
	c := crumbs("/etc/ssl")
	if len(c) != 3 || c[0].Path != "/" || c[1] != (crumb{"etc", "/etc"}) || c[2] != (crumb{"ssl", "/etc/ssl"}) {
		t.Errorf("%v", c)
	}
	if len(crumbs("/")) != 1 {
		t.Error("root")
	}
}

func TestAttachment(t *testing.T) {
	for name, want := range map[string]string{
		"a.txt":         `attachment; filename=a.txt`,
		`x"; evil=1.sh`: `attachment; filename="x\"; evil=1.sh"`,
		"été.txt":       `attachment; filename*=utf-8''%C3%A9t%C3%A9.txt`,
	} {
		got := attachment(name)
		if got != want {
			t.Errorf("attachment(%q) = %s, want %s", name, got, want)
		}
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("newline in %q", got)
		}
	}
	if got := attachment("a\r\nSet-Cookie: x"); strings.ContainsAny(got, "\r\n") {
		t.Errorf("header injection: %q", got)
	}
}

func TestIsText(t *testing.T) {
	if !isText([]byte("héllo\n")) || !isText(nil) || isText([]byte{0xff, 0xfe}) || isText([]byte("a\x00b")) {
		t.Error("isText")
	}
}

func TestFilesTemplate(t *testing.T) {
	pages, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	base := filesData{Kind: "volume", Name: "data", Base: "/volumes/data/files", Back: "/resources", Crumbs: crumbs("/a"), Path: "/a", CanWrite: true}
	dir := base
	dir.Dir = true
	dir.List = dirList{Truncated: true, Entries: []fileEntry{
		{Name: "sub", Path: "/a/sub", Dir: true, Mode: "drwxr-xr-x"},
		{Name: `x"<y>.txt`, Path: `/a/x"<y>.txt`, Size: 12, Mode: "-rw-r--r--", Mtime: time.Unix(1e9, 0)},
		{Name: "l", Path: "/a/l", Link: "../b", Mode: "lrwxrwxrwx"},
	}}
	file := base
	file.File = &fileView{Size: 3, Mode: "-rw-r--r--", Text: "\n</textarea><script>", Editable: true, CRLF: true}
	link := base
	link.Link, link.LinkPath = "../b", "/b"
	for name, d := range map[string]filesData{"dir": dir, "file": file, "link": link, "error": {Error: "Chemin introuvable.", Crumbs: crumbs("/")}} {
		var b bytes.Buffer
		if err := pages["files"].ExecuteTemplate(&b, "layout", page{Data: d}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out := b.String()
		if strings.Contains(out, "</textarea><script>") {
			t.Errorf("%s: unescaped content", name)
		}
		if name == "file" && !strings.Contains(out, ">\n\n&lt;/textarea&gt;") {
			t.Errorf("file: leading newline lost or text unescaped")
		}
		if name == "dir" && !strings.Contains(out, `href="/volumes/data/files?path=%2fa%2fx%22%3cy%3e.txt"`) {
			t.Errorf("dir: path not escaped in link:\n%s", out)
		}
	}
}

func TestSensitiveFile(t *testing.T) {
	cases := map[string]bool{
		"/run/secrets/db_password": true,
		"/app/.env":                true,
		"/certs/server.key":        true,
		"/certs/ca.PEM":            true,
		"/etc/app/api_token.txt":   true,
		"/etc/hostname":            false,
		"/var/log/app.log":         false,
		"/app/config.yaml":         false,
	}
	for p, want := range cases {
		if got := sensitiveFile(p); got != want {
			t.Errorf("sensitiveFile(%q) = %v, want %v", p, got, want)
		}
	}
}
