package server

// The file browser goes through Docker's archive endpoint, the API behind
// `docker cp`: no exec, no shell needed in the image, and stopped containers
// work too. A volume is reached through a helper container mounting it, created
// but never started, and removed at the end of each request.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/neotoxicfr/dockerr/internal/auth"
	"github.com/neotoxicfr/dockerr/internal/stacks"
)

const (
	maxListEntries = 2000      // first-level entries shown for one folder
	maxListBytes   = 256 << 20 // archive read to list one folder: it holds the whole subtree
	maxEditSize    = 1 << 20
	maxUploadSize  = 100 << 20
	helperImage    = "busybox:1.37"
	helperLabel    = "dockerr.helper"
	helperPrefix   = "dockerr-files-"
	volRoot        = "/v" // where the helper mounts the volume
)

// volNameRe is Docker's rule for volume names.
var volNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// cleanPath makes p absolute and resolves "..": the result never leaves /.
func cleanPath(p string) (string, bool) {
	if strings.ContainsRune(p, 0) {
		return "", false
	}
	return path.Clean("/" + p), true
}

// validFileName accepts an uploaded file's name: one path element.
func validFileName(n string) bool {
	return n != "" && n != "." && n != ".." && len(n) <= 255 && !strings.ContainsAny(n, "/\\\x00")
}

func isText(b []byte) bool {
	return utf8.Valid(b) && !bytes.ContainsRune(b, 0)
}

// modeString writes a mode the way ls does: -rw-r--r--, drwxrwxrwt, lrwxrwxrwx.
func modeString(m fs.FileMode) string {
	b := []byte("-rwxrwxrwx")
	switch {
	case m.IsDir():
		b[0] = 'd'
	case m&fs.ModeSymlink != 0:
		b[0] = 'l'
	case m&fs.ModeNamedPipe != 0:
		b[0] = 'p'
	case m&fs.ModeSocket != 0:
		b[0] = 's'
	case m&fs.ModeCharDevice != 0:
		b[0] = 'c'
	case m&fs.ModeDevice != 0:
		b[0] = 'b'
	}
	for i := range 9 {
		if m&(1<<uint(8-i)) == 0 {
			b[i+1] = '-'
		}
	}
	special := func(on bool, i int, x, noX byte) {
		if on {
			if b[i] == '-' {
				b[i] = noX
			} else {
				b[i] = x
			}
		}
	}
	special(m&fs.ModeSetuid != 0, 3, 's', 'S')
	special(m&fs.ModeSetgid != 0, 6, 's', 'S')
	special(m&fs.ModeSticky != 0, 9, 't', 'T')
	return string(b)
}

type fileEntry struct {
	Name  string
	Path  string // in the browser's terms: absolute, inside the volume for a volume
	Dir   bool
	Size  uint64
	Mode  string
	Mtime time.Time
	Link  string // symbolic link target
}

type dirList struct {
	Entries   []fileEntry
	Truncated bool
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// readDir lists the first level of a folder from its archive. The archive's
// first entry is the folder itself ("/" or "etc/"), then its whole subtree,
// depth first: only headers are kept, and reading stops after maxEntries
// entries or maxBytes of archive.
func readDir(r io.Reader, dir string, maxEntries int, maxBytes int64) (dirList, error) {
	var l dirList
	cr := &countingReader{r: io.LimitReader(r, maxBytes)}
	tr := tar.NewReader(cr)
	root, first := "", true
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if err != nil {
			if cr.n >= maxBytes {
				l.Truncated = true
				break
			}
			if errors.Is(err, io.EOF) {
				break
			}
			return l, fmt.Errorf("lecture de l'archive : %w", err)
		}
		name := strings.Trim(strings.TrimPrefix(h.Name, "./"), "/")
		if first {
			root, first = name, false
			continue
		}
		rest := name
		if root != "" {
			var ok bool
			if rest, ok = strings.CutPrefix(name, root+"/"); !ok {
				continue
			}
		}
		top, _, deeper := strings.Cut(rest, "/")
		if top == "" || seen[top] {
			continue
		}
		if len(l.Entries) == maxEntries {
			l.Truncated = true
			break
		}
		seen[top] = true
		e := fileEntry{Name: top, Path: path.Join(dir, top)}
		if deeper { // a folder whose own entry is missing
			e.Dir, e.Mode = true, modeString(fs.ModeDir|0o755)
		} else {
			m := h.FileInfo().Mode()
			e.Dir, e.Size, e.Mode, e.Mtime = m.IsDir(), uint64(max(h.Size, 0)), modeString(m), h.ModTime
			if h.Typeflag == tar.TypeSymlink {
				e.Link = h.Linkname
			}
		}
		l.Entries = append(l.Entries, e)
	}
	sort.SliceStable(l.Entries, func(i, j int) bool {
		a, b := l.Entries[i], l.Entries[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return a.Name < b.Name
	})
	return l, nil
}

// tarFile is one regular file of an archive to write.
type tarFile struct {
	Header tar.Header
	Body   io.Reader
}

// writeTar writes regular files: name, mode and owner come from each header.
func writeTar(w io.Writer, files []tarFile) error {
	tw := tar.NewWriter(w)
	for _, f := range files {
		h := f.Header
		h.Typeflag = tar.TypeReg
		if err := tw.WriteHeader(&h); err != nil {
			return fmt.Errorf("archive de %s : %w", h.Name, err)
		}
		if _, err := io.Copy(tw, f.Body); err != nil {
			return fmt.Errorf("lecture de %s : %w", h.Name, err)
		}
	}
	return tw.Close()
}

// replaceHeader is the header that rewrites a file with size bytes, keeping
// the original's mode and owner: Docker extracts the archive with the owner it
// carries (copyUIDGID off).
func replaceHeader(orig *tar.Header, size int64, now time.Time) tar.Header {
	return tar.Header{Name: path.Base(orig.Name), Mode: orig.Mode & 0o7777, Uid: orig.Uid, Gid: orig.Gid,
		Uname: orig.Uname, Gname: orig.Gname, Size: size, ModTime: now}
}

// linkPath resolves a symbolic link's target for the browser, "" when it
// cannot be followed: an absolute target in a volume points into the
// containers that mount it, not into the volume.
func linkPath(p, target string, volume bool) string {
	if target == "" || (volume && path.IsAbs(target)) {
		return ""
	}
	if path.IsAbs(target) {
		return path.Clean(target)
	}
	return path.Join(path.Dir(p), target)
}

type crumb struct{ Name, Path string }

func crumbs(p string) []crumb {
	out := []crumb{{"/", "/"}}
	if p == "/" {
		return out
	}
	acc := ""
	for _, part := range strings.Split(p[1:], "/") {
		acc += "/" + part
		out = append(out, crumb{part, acc})
	}
	return out
}

// fileTarget is the container the archive calls go to: the one browsed, or
// the helper of a volume.
type fileTarget struct {
	ctr   string
	root  string // volRoot for a volume, "" for a container
	Kind  string // "container" or "volume", for the logs
	Name  string
	Base  string // URL of the browser
	Back  string
	close func()
}

// real maps a browser path to the path in the target container.
func (t *fileTarget) real(p string) string {
	if t.root == "" {
		return p
	}
	return path.Join(t.root, p)
}

// fail answers a page request with text, others with JSON.
func fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if r.Method == http.MethodGet {
		http.Error(w, msg, status)
		return
	}
	jsonError(w, status, msg)
}

// openTarget checks the mode and the proxy's rights, then finds the container
// or prepares a volume's helper; the caller must call close.
func (s *Server) openTarget(w http.ResponseWriter, r *http.Request, write bool) (*fileTarget, bool) {
	if !s.advancedOnly(w, r) {
		return nil, false
	}
	caps := s.docker.Caps
	vol := r.PathValue("name")
	if !caps.Containers || ((write || vol != "") && !caps.Write) {
		fail(w, r, http.StatusForbidden, "le proxy Docker refuse cette action (CONTAINERS=1, NETWORKS=1, POST=1)")
		return nil, false
	}
	// A volume is read through a helper container: listing volumes, and
	// pulling its image when missing, must be allowed too.
	if vol != "" && (!caps.Volumes || !caps.Images) {
		fail(w, r, http.StatusForbidden, "le proxy Docker refuse l'accès aux volumes (VOLUMES=1, IMAGES=1)")
		return nil, false
	}
	if vol == "" {
		c, _, ok := s.findContainer(w, r)
		if !ok {
			return nil, false
		}
		id := c.ID[:min(12, len(c.ID))]
		return &fileTarget{ctr: c.ID, Kind: "container", Name: c.Name, Base: "/c/" + id + "/files", Back: "/c/" + id, close: func() {}}, true
	}
	if !volNameRe.MatchString(vol) {
		fail(w, r, http.StatusBadRequest, "nom de volume invalide")
		return nil, false
	}
	// Checked first: mounting a missing volume would create it.
	if _, err := s.docker.API.VolumeInspect(r.Context(), vol, client.VolumeInspectOptions{}); err != nil {
		if cerrdefs.IsNotFound(err) {
			fail(w, r, http.StatusNotFound, "volume inconnu")
		} else {
			fail(w, r, http.StatusBadGateway, err.Error())
		}
		return nil, false
	}
	id, done, err := s.helper(r.Context(), vol)
	if err != nil {
		fail(w, r, http.StatusBadGateway, err.Error())
		return nil, false
	}
	return &fileTarget{ctr: id, root: volRoot, Kind: "volume", Name: vol, Base: "/volumes/" + vol + "/files",
		Back: "/resources?tab=volumes", close: done}, true
}

// helper creates a stopped container mounting volume on volRoot, pulling its
// image when missing. done removes it, even when the request was cancelled.
func (s *Server) helper(ctx context.Context, volume string) (string, func(), error) {
	api := s.docker.API
	if _, err := api.ImageInspect(ctx, helperImage); err != nil {
		resp, err := api.ImagePull(ctx, helperImage, client.ImagePullOptions{})
		if err != nil {
			return "", nil, fmt.Errorf("téléchargement de %s : %w", helperImage, err)
		}
		err = resp.Wait(ctx)
		_ = resp.Close()
		if err != nil {
			return "", nil, fmt.Errorf("téléchargement de %s : %w", helperImage, err)
		}
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	res, err := api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: helperPrefix + hex.EncodeToString(b),
		Config: &container.Config{Image: helperImage, Cmd: []string{"true"}, NetworkDisabled: true,
			Labels: map[string]string{helperLabel: "files"}},
		HostConfig: &container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeVolume,
			Source: volume, Target: volRoot, VolumeOptions: &mount.VolumeOptions{NoCopy: true}}}},
	})
	if err != nil {
		return "", nil, fmt.Errorf("conteneur auxiliaire : %w", err)
	}
	done := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := api.ContainerRemove(ctx, res.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			slog.Warn("files helper", "id", res.ID, "err", err)
		}
	}
	return res.ID, done, nil
}

// removeHelpers removes the helpers a previous run left behind (a crash).
func (s *Server) removeHelpers(ctx context.Context) {
	api := s.docker.API
	res, err := api.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", helperLabel+"=files")})
	if err != nil {
		slog.Warn("files helpers", "err", err)
		return
	}
	for _, c := range res.Items {
		if _, err := api.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			slog.Warn("files helper", "id", c.ID, "err", err)
		} else {
			slog.Info("files helper removed", "id", c.ID)
		}
	}
}

type fileView struct {
	// Masked: a secret-looking file, shown only on demand (and logged).
	Masked   bool
	Size     uint64
	Mode     string
	Mtime    time.Time
	Text     string
	CRLF     bool
	Editable bool
	Reason   string // why the content is not shown
}

type filesData struct {
	Kind, Name, Base, Back string
	Path                   string
	Crumbs                 []crumb
	Error                  string
	Dir                    bool
	List                   dirList
	Link, LinkPath         string
	File                   *fileView
	CanWrite               bool
}

// firstEntry opens p's archive at its first entry; close the returned body.
func (s *Server) firstEntry(ctx context.Context, t *fileTarget, p string) (*tar.Header, *tar.Reader, io.ReadCloser, error) {
	res, err := s.docker.API.CopyFromContainer(ctx, t.ctr, client.CopyFromContainerOptions{SourcePath: t.real(p)})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("lecture de %s : %w", p, err)
	}
	tr := tar.NewReader(res.Content)
	h, err := tr.Next()
	if err != nil {
		_ = res.Content.Close()
		return nil, nil, nil, fmt.Errorf("lecture de l'archive : %w", err)
	}
	return h, tr, res.Content, nil
}

func (s *Server) filesPage(w http.ResponseWriter, r *http.Request) {
	p, ok := cleanPath(r.URL.Query().Get("path"))
	if !ok {
		http.Error(w, "chemin invalide", http.StatusBadRequest)
		return
	}
	t, ok := s.openTarget(w, r, false)
	if !ok {
		return
	}
	defer t.close()
	ctx := r.Context()
	d := filesData{Kind: t.Kind, Name: t.Name, Base: t.Base, Back: t.Back, Path: p, Crumbs: crumbs(p), CanWrite: s.docker.Caps.Write}
	st, err := s.docker.API.ContainerStatPath(ctx, t.ctr, client.ContainerStatPathOptions{Path: t.real(p)})
	switch {
	case err != nil && cerrdefs.IsNotFound(err):
		d.Error = "Chemin introuvable."
	case err != nil:
		d.Error = err.Error()
	case st.Stat.Mode&fs.ModeSymlink != 0:
		d.Link, d.LinkPath = st.Stat.LinkTarget, linkPath(p, st.Stat.LinkTarget, t.root != "")
	case st.Stat.Mode.IsDir():
		d.Dir = true
		res, err := s.docker.API.CopyFromContainer(ctx, t.ctr, client.CopyFromContainerOptions{SourcePath: t.real(p)})
		if err != nil {
			d.Error = err.Error()
			break
		}
		d.List, err = readDir(res.Content, p, maxListEntries, maxListBytes)
		_ = res.Content.Close()
		if err != nil {
			d.Error = err.Error()
		}
	default:
		reveal := r.URL.Query().Get("reveal") == "1"
		if sensitiveFile(p) && !reveal {
			d.File = &fileView{Masked: true, Size: uint64(max(st.Stat.Size, 0)), Mode: modeString(st.Stat.Mode), Mtime: st.Stat.Mtime}
			break
		}
		d.File = s.fileView(ctx, t, p, st.Stat)
		if sensitiveFile(p) {
			slog.Info("file revealed", "user", auth.User(ctx), t.Kind, t.Name, "path", p)
		}
	}
	title := t.Name
	if p != "/" {
		title = path.Base(p) + " · " + t.Name
	}
	s.render(w, r, "files", title, "resources", d)
}

func (s *Server) fileView(ctx context.Context, t *fileTarget, p string, st container.PathStat) *fileView {
	f := &fileView{Size: uint64(max(st.Size, 0)), Mode: modeString(st.Mode), Mtime: st.Mtime}
	switch {
	case !st.Mode.IsRegular():
		f.Reason = "Ce n'est pas un fichier ordinaire."
		return f
	case st.Size > maxEditSize:
		f.Reason = "Trop gros pour être affiché (plus de 1 Mio)."
		return f
	}
	_, tr, body, err := s.firstEntry(ctx, t, p)
	if err != nil {
		f.Reason = err.Error()
		return f
	}
	b, err := io.ReadAll(io.LimitReader(tr, maxEditSize+1))
	_ = body.Close()
	switch {
	case err != nil:
		f.Reason = err.Error()
	case len(b) > maxEditSize:
		f.Reason = "Trop gros pour être affiché (plus de 1 Mio)."
	case !isText(b):
		f.Reason = "Fichier binaire : téléchargement seulement."
	default:
		f.Text, f.CRLF, f.Editable = string(b), bytes.Contains(b, []byte("\r\n")), s.docker.Caps.Write
	}
	return f
}

// attachment is a Content-Disposition that any file name is safe in.
func attachment(name string) string {
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": name}); v != "" {
		return v
	}
	return "attachment"
}

// filesDownload sends a file as it is, a folder as a tar archive.
func (s *Server) filesDownload(w http.ResponseWriter, r *http.Request) {
	p, ok := cleanPath(r.URL.Query().Get("path"))
	if !ok {
		http.Error(w, "chemin invalide", http.StatusBadRequest)
		return
	}
	t, ok := s.openTarget(w, r, false)
	if !ok {
		return
	}
	defer t.close()
	ctx := r.Context()
	st, err := s.docker.API.ContainerStatPath(ctx, t.ctr, client.ContainerStatPathOptions{Path: t.real(p)})
	if err != nil {
		http.Error(w, "chemin introuvable", http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Cache-Control", "no-store")
	switch {
	case st.Stat.Mode.IsDir():
		name := path.Base(p)
		if p == "/" {
			name = t.Name
		}
		res, err := s.docker.API.CopyFromContainer(ctx, t.ctr, client.CopyFromContainerOptions{SourcePath: t.real(p)})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer res.Content.Close()
		h.Set("Content-Type", "application/x-tar")
		h.Set("Content-Disposition", attachment(name+".tar"))
		_, _ = io.Copy(w, res.Content)
	case st.Stat.Mode.IsRegular():
		hdr, tr, body, err := s.firstEntry(ctx, t, p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer body.Close()
		h.Set("Content-Length", fmt.Sprint(hdr.Size))
		h.Set("Content-Disposition", attachment(path.Base(p)))
		_, _ = io.Copy(w, tr)
	default:
		http.Error(w, "ni un fichier ni un dossier", http.StatusBadRequest)
	}
}

type saveFileBody struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// filesSave rewrites a text file, keeping its mode and owner.
func (s *Server) filesSave(w http.ResponseWriter, r *http.Request) {
	var b saveFileBody
	r.Body = http.MaxBytesReader(w, r.Body, 4*maxEditSize)
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		jsonError(w, http.StatusBadRequest, "requête invalide")
		return
	}
	p, ok := cleanPath(b.Path)
	if !ok || p == "/" {
		jsonError(w, http.StatusBadRequest, "chemin invalide")
		return
	}
	if len(b.Content) > maxEditSize {
		jsonError(w, http.StatusRequestEntityTooLarge, "plus de 1 Mio")
		return
	}
	t, ok := s.openTarget(w, r, true)
	if !ok {
		return
	}
	defer t.close()
	ctx := r.Context()
	orig, _, body, err := s.firstEntry(ctx, t, p)
	if err != nil {
		jsonError(w, http.StatusNotFound, "fichier introuvable")
		return
	}
	_ = body.Close()
	if orig.Typeflag != tar.TypeReg {
		jsonError(w, http.StatusBadRequest, "ce n'est pas un fichier ordinaire")
		return
	}
	var buf bytes.Buffer
	if err := writeTar(&buf, []tarFile{{replaceHeader(orig, int64(len(b.Content)), time.Now()), strings.NewReader(b.Content)}}); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := s.docker.API.CopyToContainer(ctx, t.ctr, client.CopyToContainerOptions{DestinationPath: t.real(path.Dir(p)), Content: &buf}); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	slog.Info("file saved", "user", auth.User(ctx), t.Kind, t.Name, "path", p, "bytes", len(b.Content))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// filesUpload writes the uploaded files into ?path=, owned by the folder's
// owner. Files that exist already are replaced only with overwrite=1.
func (s *Server) filesUpload(w http.ResponseWriter, r *http.Request) {
	dir, ok := cleanPath(r.URL.Query().Get("path"))
	if !ok {
		jsonError(w, http.StatusBadRequest, "chemin invalide")
		return
	}
	t, ok := s.openTarget(w, r, true)
	if !ok {
		return
	}
	defer t.close()
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		if mbe := (*http.MaxBytesError)(nil); errors.As(err, &mbe) {
			jsonError(w, http.StatusRequestEntityTooLarge, "plus de 100 Mio")
		} else {
			jsonError(w, http.StatusBadRequest, "requête invalide")
		}
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	fhs := r.MultipartForm.File["files"]
	if len(fhs) == 0 {
		jsonError(w, http.StatusBadRequest, "aucun fichier")
		return
	}
	var total int64
	names := map[string]bool{}
	for _, fh := range fhs {
		if !validFileName(fh.Filename) || names[fh.Filename] {
			jsonError(w, http.StatusBadRequest, "nom de fichier invalide : "+fh.Filename)
			return
		}
		names[fh.Filename] = true
		total += fh.Size
	}
	if total > maxUploadSize {
		jsonError(w, http.StatusRequestEntityTooLarge, "plus de 100 Mio")
		return
	}
	ctx := r.Context()
	dh, _, body, err := s.firstEntry(ctx, t, dir)
	if err != nil {
		jsonError(w, http.StatusNotFound, "dossier introuvable")
		return
	}
	_ = body.Close()
	if dh.Typeflag != tar.TypeDir {
		jsonError(w, http.StatusBadRequest, "ce n'est pas un dossier")
		return
	}
	var exists []string
	existing := map[string]os.FileMode{} // replaced files keep their mode
	for _, fh := range fhs {
		st, err := s.docker.API.ContainerStatPath(ctx, t.ctr, client.ContainerStatPathOptions{Path: t.real(path.Join(dir, fh.Filename))})
		if err != nil && cerrdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			// Not knowing whether it exists must not skip the confirmation.
			jsonError(w, http.StatusBadGateway, "état de "+fh.Filename+" illisible : "+err.Error())
			return
		}
		if !st.Stat.Mode.IsRegular() {
			jsonError(w, http.StatusConflict, "existe et n'est pas un fichier ordinaire : "+fh.Filename)
			return
		}
		exists = append(exists, fh.Filename)
		existing[fh.Filename] = st.Stat.Mode.Perm()
	}
	if len(exists) > 0 && r.URL.Query().Get("overwrite") != "1" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "fichiers existants", "exists": exists})
		return
	}
	now := time.Now()
	files := make([]tarFile, 0, len(fhs))
	for _, fh := range fhs {
		f, err := fh.Open()
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer func() { _ = f.Close() }()
		h := tar.Header{Name: fh.Filename, Mode: 0o644, Uid: dh.Uid, Gid: dh.Gid, Size: fh.Size, ModTime: now}
		if m, ok := existing[fh.Filename]; ok {
			h.Mode = int64(m)
			if oh, _, body, err := s.firstEntry(ctx, t, path.Join(dir, fh.Filename)); err == nil {
				h.Uid, h.Gid = oh.Uid, oh.Gid // and their owner
				_ = body.Close()
			}
		}
		files = append(files, tarFile{h, f})
	}
	pr, pw := io.Pipe()
	go func() { _ = pw.CloseWithError(writeTar(pw, files)) }()
	_, err = s.docker.API.CopyToContainer(ctx, t.ctr, client.CopyToContainerOptions{DestinationPath: t.real(dir), Content: pr})
	_ = pr.CloseWithError(errors.New("upload done"))
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	for _, fh := range fhs {
		slog.Info("file uploaded", "user", auth.User(ctx), t.Kind, t.Name, "path", path.Join(dir, fh.Filename), "bytes", fh.Size)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// sensitiveFile: the content of these files stays hidden until asked for,
// like secrets elsewhere in dockerr.
func sensitiveFile(p string) bool {
	if strings.HasPrefix(p, "/run/secrets/") {
		return true
	}
	b := strings.ToLower(path.Base(p))
	for _, ext := range []string{".key", ".pem", ".p12", ".pfx", ".env"} {
		if strings.HasSuffix(b, ext) {
			return true
		}
	}
	return b == ".env" || stacks.SensitiveKey.MatchString(strings.TrimSuffix(b, path.Ext(b)))
}
