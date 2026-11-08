package engine

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/Gitguy268/usb-llms/internal/drive"
	"github.com/Gitguy268/usb-llms/internal/sys"
)

// Version is the Ollama release this build was tested against.
const Version = "v0.40.13"

const releases = "https://github.com/ollama/ollama/releases/download/"

// Variant says how much of the engine goes on the drive.
type Variant string

const (
	// Lean leaves out the NVIDIA and AMD libraries. It runs on CPU, Vulkan and Metal.
	Lean Variant = "lean"
	// Full keeps every GPU library, about 1.5 GB more on Linux and Windows.
	Full Variant = "full"
)

// InstallOptions configures Install. Base overrides the release URL in tests.
type InstallOptions struct {
	Variant  Variant
	Base     string
	Progress func(done, total int64)
}

// Manifest records what is installed, in engine/<platform>/engine.json.
type Manifest struct {
	Version string  `json:"version"`
	Variant Variant `json:"variant"`
	Binary  string  `json:"binary"`
}

// DefaultVariant picks full only when this machine has a GPU that needs those libraries.
func DefaultVariant() Variant {
	if nvidia, amd := sys.HasDedicatedGPU(); nvidia || amd {
		return Full
	}
	return Lean
}

// Assets lists the release archives for a platform and variant.
func Assets(goos, goarch string, v Variant, amd bool) ([]string, error) {
	switch goos + "/" + goarch {
	case "darwin/arm64", "darwin/amd64":
		return []string{"ollama-darwin.tgz"}, nil
	case "linux/amd64", "linux/arm64":
		out := []string{"ollama-linux-" + goarch + ".tar.zst"}
		if v == Full && amd && goarch == "amd64" {
			out = append(out, "ollama-linux-amd64-rocm.tar.zst")
		}
		return out, nil
	case "windows/amd64":
		out := []string{"ollama-windows-amd64.zip"}
		if v == Full && amd {
			out = append(out, "ollama-windows-amd64-rocm.zip")
		}
		return out, nil
	case "windows/arm64":
		return []string{"ollama-windows-arm64.zip"}, nil
	}
	return nil, fmt.Errorf("Ollama has no build for %s/%s", goos, goarch)
}

var gpuPart = regexp.MustCompile(`(?i)^(cuda|rocm|jetpack)|^(libcu|libnv|libam|libhsa)`)

// Keep decides whether an archive entry belongs in the variant.
func Keep(name string, v Variant) bool {
	if v == Full {
		return true
	}
	for _, part := range strings.Split(path.Clean(strings.ReplaceAll(name, `\`, "/")), "/") {
		if gpuPart.MatchString(part) {
			return false
		}
	}
	return true
}

// Installed reads the engine manifest for this machine.
func Installed(d *drive.Drive) (Manifest, bool) {
	var m Manifest
	raw, err := os.ReadFile(filepath.Join(d.Engine(), "engine.json"))
	if err != nil || json.Unmarshal(raw, &m) != nil || m.Binary == "" {
		return m, false
	}
	_, err = os.Stat(filepath.Join(d.Engine(), m.Binary))
	return m, err == nil
}

// Install puts the engine on the drive. It unpacks into a temp folder and swaps it in only
// after every checksum matches, so an interrupted run leaves no half-working engine.
func Install(ctx context.Context, d *drive.Drive, client *http.Client, o InstallOptions) (Manifest, error) {
	base := o.Base
	if base == "" {
		base = releases + Version + "/"
	}
	if o.Variant == "" {
		o.Variant = DefaultVariant()
	}
	_, amd := sys.HasDedicatedGPU()
	assets, err := Assets(runtime.GOOS, runtime.GOARCH, o.Variant, amd)
	if err != nil {
		return Manifest{}, err
	}
	sums, err := fetchSums(ctx, client, base)
	if err != nil {
		return Manifest{}, err
	}
	target := d.Engine()
	tmp := target + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return Manifest{}, err
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(tmp)
	for _, asset := range assets {
		want, ok := sums[asset]
		if !ok {
			return Manifest{}, fmt.Errorf("%s is missing from the release checksums", asset)
		}
		if err := fetchAsset(ctx, d, client, base+asset, asset, want, tmp, o); err != nil {
			return Manifest{}, fmt.Errorf("%s: %w", asset, err)
		}
	}
	bin, err := findBinary(tmp)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{Version: Version, Variant: o.Variant, Binary: bin}
	raw, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, "engine.json"), raw, 0o644); err != nil {
		return Manifest{}, err
	}
	if err := os.RemoveAll(target); err != nil {
		return Manifest{}, err
	}
	return m, os.Rename(tmp, target)
}

func fetchSums(ctx context.Context, client *http.Client, base string) (map[string]string, error) {
	body, err := get(ctx, client, base+"sha256sum.txt", 0)
	if err != nil {
		return nil, fmt.Errorf("checksums: %w", err)
	}
	defer body.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(body)
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 {
			out[strings.TrimPrefix(f[1], "./")] = f[0]
		}
	}
	return out, sc.Err()
}

type response struct {
	io.ReadCloser
	size int64
}

func get(ctx context.Context, client *http.Client, url string, from int64) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if from > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", from))
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		res.Body.Close()
		return nil, fmt.Errorf("download failed: HTTP %d", res.StatusCode)
	}
	if from > 0 && res.StatusCode != http.StatusPartialContent {
		res.Body.Close()
		return nil, errNoResume
	}
	return &response{res.Body, res.ContentLength}, nil
}

var errNoResume = errors.New("server does not support resume")

type counter struct {
	r        io.Reader
	done     int64
	total    int64
	progress func(int64, int64)
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.done += int64(n)
	if c.progress != nil {
		c.progress(c.done, c.total)
	}
	return n, err
}

func fetchAsset(ctx context.Context, d *drive.Drive, client *http.Client, url, name, want, dest string, o InstallOptions) error {
	if strings.HasSuffix(name, ".zip") {
		return fetchZip(ctx, d, client, url, name, want, dest, o)
	}
	body, err := get(ctx, client, url, 0)
	if err != nil {
		return err
	}
	defer body.Close()
	hash := sha256.New()
	stream := io.TeeReader(&counter{r: body, total: body.size, progress: o.Progress}, hash)
	var plain io.Reader
	if strings.HasSuffix(name, ".zst") {
		dec, err := zstd.NewReader(stream)
		if err != nil {
			return err
		}
		defer dec.Close()
		plain = dec
	} else {
		gz, err := gzip.NewReader(stream)
		if err != nil {
			return err
		}
		defer gz.Close()
		plain = gz
	}
	if err := untar(plain, dest, o.Variant); err != nil {
		return err
	}
	// Hash the archive's tail too: tar stops reading at its end marker.
	if _, err := io.Copy(io.Discard, stream); err != nil {
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, want)
	}
	return nil
}

// fetchZip keeps the archive on the drive while it works, because a zip can't be read as a stream.
func fetchZip(ctx context.Context, d *drive.Drive, client *http.Client, url, name, want, dest string, o InstallOptions) error {
	part := filepath.Join(d.Tmp(), name+".part")
	if err := Download(ctx, client, url, part, o.Progress); err != nil {
		return err
	}
	if got, err := hashFile(part); err != nil {
		return err
	} else if got != want {
		os.Remove(part)
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, want)
	}
	defer os.Remove(part)
	zr, err := zip.OpenReader(part)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !Keep(f.Name, o.Variant) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeEntry(dest, f.Name, rc, f.Mode())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// Download saves url to path, continuing a partial file if one is there.
func Download(ctx context.Context, client *http.Client, url, path string, progress func(done, total int64)) error {
	var from int64
	if st, err := os.Stat(path); err == nil {
		from = st.Size()
	}
	body, err := get(ctx, client, url, from)
	if errors.Is(err, errNoResume) {
		from = 0
		body, err = get(ctx, client, url, 0)
	}
	if err != nil {
		return err
	}
	defer body.Close()
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if from == 0 {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, &counter{r: body, done: from, total: from + body.size, progress: progress})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func untar(r io.Reader, dest string, v Variant) error {
	type link struct{ path, target string }
	var links []link
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if !Keep(h.Name, v) {
			continue
		}
		switch h.Typeflag {
		case tar.TypeReg:
			if err := writeEntry(dest, h.Name, tr, h.FileInfo().Mode()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			links = append(links, link{h.Name, h.Linkname})
		}
	}
	// A link may point at another link, so keep going until a pass resolves nothing new.
	pending := links
	for len(pending) > 0 {
		var next []link
		for _, l := range pending {
			if err := placeLink(dest, l.path, l.target); err != nil {
				next = append(next, l)
			}
		}
		if len(next) == len(pending) {
			return fmt.Errorf("can't resolve link %s -> %s", next[0].path, next[0].target)
		}
		pending = next
	}
	return nil
}

// placeLink makes a symlink, or a plain copy on file systems without them, such as exFAT.
func placeLink(dest, name, target string) error {
	p, err := safeJoin(dest, name)
	if err != nil {
		return err
	}
	resolved := filepath.Join(filepath.Dir(p), filepath.FromSlash(target))
	rel, err := filepath.Rel(dest, resolved)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("link %s points outside the engine", name)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(resolved); err != nil {
		return err
	}
	if err := os.Symlink(filepath.FromSlash(target), p); err == nil {
		return nil
	}
	src, err := os.Open(resolved)
	if err != nil {
		return err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return err
	}
	if st.IsDir() {
		return nil
	}
	return writeEntry(dest, name, src, st.Mode())
}

func safeJoin(dest, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(name, `\`, "/")))
	if !filepath.IsLocal(clean) {
		return "", fmt.Errorf("archive entry %q points outside the engine", name)
	}
	return filepath.Join(dest, clean), nil
}

func writeEntry(dest, name string, r io.Reader, mode os.FileMode) error {
	p, err := safeJoin(dest, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_ = os.Chmod(p, mode|0o700) // exFAT has no permission bits, so a failure here is fine
	return nil
}

func findBinary(root string) (string, error) {
	name := "ollama"
	if runtime.GOOS == "windows" {
		name = "ollama.exe"
	}
	for _, rel := range []string{name, filepath.Join("bin", name)} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			return rel, nil
		}
	}
	return "", errors.New("the engine archive has no ollama binary")
}

// Unpack extracts a downloaded .tar.gz or .zip into dest, with the same safety checks as the engine.
func Unpack(archive, dest string) error {
	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer zr.Close()
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			err = writeEntry(dest, f.Name, rc, f.Mode())
			rc.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	return untar(gz, dest, Full)
}

// HashFile returns a file's SHA-256 in hex.
func HashFile(path string) (string, error) { return hashFile(path) }
