// Package webui installs and runs Open WebUI on the drive, unmodified, through uv.
package webui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Gitguy268/usb-llms/internal/drive"
	"github.com/Gitguy268/usb-llms/internal/engine"
	"github.com/Gitguy268/usb-llms/internal/sys"
)

const (
	// Version is the Open WebUI release to install.
	Version = "0.12.5"
	// Python is the newest version Open WebUI supports.
	Python = "3.12"
	// Port avoids Open WebUI's default 8080, which is often taken.
	Port = 11437

	uvVersion = "0.13.6"
	needBytes = 4_000_000_000
	deadProxy = "http://127.0.0.1:9"
)

func platformDir(d *drive.Drive) string { return filepath.Join(d.WebUI(), drive.Platform()) }

func venvBin(d *drive.Drive, name string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(platformDir(d), "venv", "Scripts", name+".exe")
	}
	return filepath.Join(platformDir(d), "venv", "bin", name)
}

// Installed reports whether Open WebUI is ready for this machine.
func Installed(d *drive.Drive) bool {
	_, err := os.Stat(venvBin(d, "open-webui"))
	return err == nil
}

func uvEnv(base []string, d *drive.Drive) []string {
	return append(base,
		"UV_PYTHON_INSTALL_DIR="+filepath.Join(platformDir(d), "python"),
		"UV_CACHE_DIR="+filepath.Join(d.WebUI(), "uv-cache"),
		"UV_PYTHON_BIN_DIR="+filepath.Join(platformDir(d), "python-bin"),
		"UV_LINK_MODE=copy",
		"UV_PYTHON_PREFERENCE=only-managed",
		"TMPDIR="+d.Tmp(), "TEMP="+d.Tmp(), "TMP="+d.Tmp(),
	)
}

// UVAsset names the uv release archive for a platform.
func UVAsset(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "darwin/arm64":
		return "uv-aarch64-apple-darwin.tar.gz", nil
	case "darwin/amd64":
		return "uv-x86_64-apple-darwin.tar.gz", nil
	case "linux/amd64":
		return "uv-x86_64-unknown-linux-gnu.tar.gz", nil
	case "linux/arm64":
		return "uv-aarch64-unknown-linux-gnu.tar.gz", nil
	case "windows/amd64":
		return "uv-x86_64-pc-windows-msvc.zip", nil
	case "windows/arm64":
		return "uv-aarch64-pc-windows-msvc.zip", nil
	}
	return "", fmt.Errorf("uv has no build for %s/%s", goos, goarch)
}

func findUV(ctx context.Context, d *drive.Drive, client *http.Client) (string, error) {
	name := "uv"
	if runtime.GOOS == "windows" {
		name = "uv.exe"
	}
	dir := filepath.Join(platformDir(d), "uv")
	found := func() string {
		hit := ""
		_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
			if err == nil && !e.IsDir() && e.Name() == name {
				hit = p
				return filepath.SkipAll
			}
			return nil
		})
		return hit
	}
	if p := found(); p != "" {
		return p, nil
	}
	asset, err := UVAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	base := "https://github.com/astral-sh/uv/releases/download/" + uvVersion + "/" + asset
	req, err := http.NewRequestWithContext(ctx, "GET", base+".sha256", nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1024))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(raw))
	if res.StatusCode != 200 || len(fields) == 0 {
		return "", fmt.Errorf("couldn't read uv's checksum (HTTP %d)", res.StatusCode)
	}
	archive := filepath.Join(d.Tmp(), asset)
	defer os.Remove(archive)
	if err := engine.Download(ctx, client, base, archive, nil); err != nil {
		return "", err
	}
	if got, err := engine.HashFile(archive); err != nil {
		return "", err
	} else if got != fields[0] {
		return "", fmt.Errorf("uv failed its checksum")
	}
	if err := engine.Unpack(archive, dir); err != nil {
		return "", err
	}
	if p := found(); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("the uv archive had no %s", name)
}

// Install fetches uv, Python and Open WebUI onto the drive, then stores the embedding model
// document search needs, so a first offline start doesn't try to download it.
func Install(ctx context.Context, d *drive.Drive, client *http.Client, out io.Writer) error {
	if !d.Online() {
		return fmt.Errorf("installing Open WebUI needs the internet; run `usb-llms mode online` first")
	}
	if free, err := sys.FreeBytes(d.Root); err == nil && free < needBytes {
		return fmt.Errorf("Open WebUI needs about %s, and the drive has %s free", sys.GB(needBytes), sys.GB(free))
	}
	if err := os.MkdirAll(platformDir(d), 0o755); err != nil {
		return err
	}
	uv, err := findUV(ctx, d, client)
	if err != nil {
		return fmt.Errorf("uv: %w", err)
	}
	venv := filepath.Join(platformDir(d), "venv")
	run := func(extra []string, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = uvEnv(append(os.Environ(), extra...), d)
		cmd.Stdout, cmd.Stderr = out, out
		return cmd.Run()
	}
	steps := [][]string{
		{"python", "install", Python},
		{"venv", "--clear", "--python", Python, venv},
		{"pip", "install", "--python", venvBin(d, "python"), "open-webui==" + Version},
	}
	for _, args := range steps {
		if err := run(nil, uv, args...); err != nil {
			return fmt.Errorf("uv %s: %w", strings.Join(args[:2], " "), err)
		}
	}
	_ = os.RemoveAll(filepath.Join(d.WebUI(), "uv-cache"))
	if err := run(cacheEnv(d), venvBin(d, "python"), "-c", "from sentence_transformers import SentenceTransformer as S; S('sentence-transformers/all-MiniLM-L6-v2')"); err != nil {
		fmt.Fprintf(out, "Document search won't work offline, because its model couldn't be stored: %v\n", err)
	}
	return nil
}

func cacheEnv(d *drive.Drive) []string {
	cache := filepath.Join(d.WebUI(), "cache")
	return []string{"HF_HOME=" + cache, "SENTENCE_TRANSFORMERS_HOME=" + filepath.Join(cache, "st")}
}

// Env builds Open WebUI's environment. Offline uses its own OFFLINE_MODE plus a dead proxy for anything that ignores it.
func Env(base []string, d *drive.Drive, engineURL, secret string, online bool) []string {
	out := make([]string, 0, len(base)+20)
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(key) {
		case "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "OPENAI_API_KEY":
			if !online {
				continue
			}
		}
		out = append(out, kv)
	}
	home := filepath.Join(d.WebUI(), "home")
	out = append(out,
		"DATA_DIR="+filepath.Join(d.WebUI(), "data"),
		"OLLAMA_BASE_URL="+engineURL,
		"WEBUI_SECRET_KEY="+secret,
		"WEBUI_AUTH=False",
		"HOME="+home, "USERPROFILE="+home,
		"TMPDIR="+d.Tmp(), "TEMP="+d.Tmp(), "TMP="+d.Tmp(),
		"SCARF_NO_ANALYTICS=true", "DO_NOT_TRACK=true", "ANONYMIZED_TELEMETRY=False",
	)
	out = append(out, cacheEnv(d)...)
	if !online {
		out = append(out,
			"OFFLINE_MODE=true", "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "ENABLE_OPENAI_API=False",
			"HTTP_PROXY="+deadProxy, "HTTPS_PROXY="+deadProxy, "ALL_PROXY="+deadProxy, "NO_PROXY=localhost,127.0.0.1,::1",
		)
	}
	return out
}

// Secret returns the session-signing key kept on the drive, creating it once.
func Secret(d *drive.Drive) (string, error) {
	path := filepath.Join(d.WebUI(), "secret-key")
	if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
		return strings.TrimSpace(string(raw)), nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	if err := os.MkdirAll(d.WebUI(), 0o755); err != nil {
		return "", err
	}
	s := hex.EncodeToString(b)
	return s, os.WriteFile(path, []byte(s), 0o600)
}

// seedVectorDB creates Chroma's database before Open WebUI opens it. exFAT gives a file a new
// ID when its first byte is written, and SQLite then refuses to write, taking the file for
// moved ("attempt to write a readonly database"). A file that already has data keeps its ID.
func seedVectorDB(ctx context.Context, d *drive.Drive) error {
	db := filepath.Join(d.WebUI(), "data", "vector_db", "chroma.sqlite3")
	if st, err := os.Stat(db); err == nil && st.Size() > 0 {
		return nil
	}
	script := "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); c.execute('create table seed(a)'); c.execute('drop table seed'); c.commit(); c.close()"
	return exec.CommandContext(ctx, venvBin(d, "python"), "-I", "-c", script, db).Run()
}

// Command prepares `open-webui serve` on localhost.
func Command(ctx context.Context, d *drive.Drive, engineURL string, online bool) (*exec.Cmd, error) {
	if !Installed(d) {
		return nil, fmt.Errorf("Open WebUI isn't on this drive for %s yet; run `usb-llms webui install` while online", drive.Platform())
	}
	secret, err := Secret(d)
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{filepath.Join(d.WebUI(), "data", "vector_db"), filepath.Join(d.WebUI(), "home")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if err := seedVectorDB(ctx, d); err != nil {
		return nil, fmt.Errorf("couldn't prepare Open WebUI's database: %w", err)
	}
	cmd := exec.CommandContext(ctx, venvBin(d, "open-webui"), "serve", "--host", "127.0.0.1", "--port", fmt.Sprint(Port))
	cmd.Env = Env(os.Environ(), d, engineURL, secret, online)
	cmd.Dir = filepath.Join(d.WebUI(), "home")
	return cmd, nil
}

// FindUV returns uv on the drive for this machine, downloading and checking it once.
func FindUV(ctx context.Context, d *drive.Drive, client *http.Client) (string, error) {
	return findUV(ctx, d, client)
}
