package engine

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/Gitguy268/usb-llms/internal/drive"
	"github.com/Gitguy268/usb-llms/internal/netguard"
)

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

func TestEnvKeepsEverythingOnTheDrive(t *testing.T) {
	d, _ := drive.Find(t.TempDir())
	base := []string{"PATH=/bin", "HOME=/Users/me", "OLLAMA_MODELS=/Users/me/.ollama", "OLLAMA_HOST=0.0.0.0", "HTTPS_PROXY=http://corp:1"}
	got := envMap(Env(base, d, 11435, true))
	if got["HOME"] != d.EngineHome() || got["USERPROFILE"] != d.EngineHome() {
		t.Errorf("home should be on the drive: %v", got["HOME"])
	}
	if got["OLLAMA_MODELS"] != d.Models() || got["OLLAMA_HOST"] != "127.0.0.1:11435" {
		t.Errorf("engine settings wrong: %v", got)
	}
	if got["TMPDIR"] != d.Tmp() || got["PATH"] != "/bin" {
		t.Errorf("temp or path wrong: %v", got)
	}
	if got["HTTPS_PROXY"] != "" {
		t.Error("the host's proxy should not reach the engine online")
	}
	if got["OLLAMA_NO_CLOUD"] != "" {
		t.Error("online should leave cloud features on")
	}
}

func TestEnvOfflineBlocksEngineTraffic(t *testing.T) {
	d, _ := drive.Find(t.TempDir())
	got := envMap(Env(nil, d, 11435, false))
	if got["OLLAMA_NO_CLOUD"] != "1" || got["HTTPS_PROXY"] != deadProxy || got["HTTP_PROXY"] != deadProxy {
		t.Errorf("offline env wrong: %v", got)
	}
	if !strings.Contains(got["NO_PROXY"], "127.0.0.1") {
		t.Error("loopback must bypass the dead proxy")
	}
}

func TestKeep(t *testing.T) {
	cases := map[string]bool{
		"bin/ollama":                      true,
		"lib/ollama/libggml-base.so":      true,
		"lib/ollama/vulkan/libggml-vk.so": true,
		"lib/ollama/cuda_v12/libggml.so":  false,
		"lib/ollama/cuda_v13/x.so":        false,
		"lib/ollama/rocm/libggml.so":      false,
		"lib/ollama/libcublas.so.12":      false,
		`lib\ollama\cuda_v12\ggml.dll`:    false,
		"ollama":                          true,
	}
	for name, want := range cases {
		if Keep(name, Lean) != want {
			t.Errorf("Keep(%q, lean) != %v", name, want)
		}
		if !Keep(name, Full) {
			t.Errorf("full should keep %q", name)
		}
	}
}

func TestAssets(t *testing.T) {
	got, _ := Assets("linux", "amd64", Lean, true)
	if len(got) != 1 || got[0] != "ollama-linux-amd64.tar.zst" {
		t.Error(got)
	}
	got, _ = Assets("linux", "amd64", Full, true)
	if len(got) != 2 {
		t.Error("full on AMD needs the ROCm add-on", got)
	}
	got, _ = Assets("darwin", "arm64", Lean, false)
	if got[0] != "ollama-darwin.tgz" {
		t.Error(got)
	}
	if _, err := Assets("plan9", "386", Lean, false); err == nil {
		t.Error("unknown platform should fail")
	}
}

type entry struct{ name, body, link string }

func tarBytes(entries []entry) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		if e.link != "" {
			tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: tar.TypeSymlink, Linkname: e.link, Mode: 0o777})
			continue
		}
		tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: tar.TypeReg, Size: int64(len(e.body)), Mode: 0o755})
		tw.Write([]byte(e.body))
	}
	tw.Close()
	return buf.Bytes()
}

func gz(raw []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(raw)
	w.Close()
	return buf.Bytes()
}

func zst(raw []byte) []byte {
	var buf bytes.Buffer
	w, _ := zstd.NewWriter(&buf)
	w.Write(raw)
	w.Close()
	return buf.Bytes()
}

func zipBytes(entries []entry) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		f, _ := w.Create(e.name)
		f.Write([]byte(e.body))
	}
	w.Close()
	return buf.Bytes()
}

// release serves every real asset name with the same archive, so Install works on any test machine.
func release(t *testing.T, tgz, zstBytes, zipped []byte, badSum bool) string {
	t.Helper()
	files := map[string][]byte{"ollama-darwin.tgz": tgz, "ollama-linux-amd64.tar.zst": zstBytes, "ollama-linux-arm64.tar.zst": zstBytes, "ollama-windows-amd64.zip": zipped, "ollama-windows-arm64.zip": zipped}
	sums := ""
	for name, raw := range files {
		sum := sha256.Sum256(raw)
		hexSum := hex.EncodeToString(sum[:])
		if badSum {
			hexSum = strings.Repeat("0", 64)
		}
		sums += fmt.Sprintf("%s  ./%s\n", hexSum, name)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "sha256sum.txt" {
			fmt.Fprint(w, sums)
			return
		}
		if raw, ok := files[name]; ok {
			w.Write(raw)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/"
}

var goodEntries = []entry{
	{name: "ollama", body: "#!/bin/sh\n"},
	{name: "ollama.exe", body: "MZ"},
	{name: "lib/libggml.0.dylib", body: "real"},
	{name: "lib/libggml.dylib", link: "libggml.0.dylib"},
	{name: "lib/chain.dylib", link: "libggml.dylib"},
	{name: "lib/cuda_v12/huge.so", body: "gpu"},
}

func TestInstallLeanAndLinks(t *testing.T) {
	d, _ := drive.Find(t.TempDir())
	tarred := tarBytes(goodEntries)
	base := release(t, gz(tarred), zst(tarred), zipBytes(goodEntries), false)
	m, err := Install(context.Background(), d, netguard.Client(true), InstallOptions{Variant: Lean, Base: base})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := Installed(d); !ok || got.Variant != Lean {
		t.Errorf("manifest: %+v %v", m, ok)
	}
	if _, err := os.Stat(filepath.Join(d.Engine(), "lib", "cuda_v12")); err == nil {
		t.Error("lean should leave out GPU libraries")
	}
	if !strings.Contains(os.Getenv("GOOS"), "windows") {
		if _, err := os.Stat(filepath.Join(d.Engine(), "lib", "libggml.0.dylib")); err == nil {
			raw, err := os.ReadFile(filepath.Join(d.Engine(), "lib", "chain.dylib"))
			if err != nil || string(raw) != "real" {
				t.Errorf("a link chain should resolve: %q %v", raw, err)
			}
		}
	}
	if _, err := os.Stat(d.Engine() + ".tmp"); err == nil {
		t.Error("temp folder should be gone")
	}
}

func TestInstallRejectsBadChecksumAndKeepsOldEngine(t *testing.T) {
	d, _ := drive.Find(t.TempDir())
	tarred := tarBytes(goodEntries)
	good := release(t, gz(tarred), zst(tarred), zipBytes(goodEntries), false)
	if _, err := Install(context.Background(), d, netguard.Client(true), InstallOptions{Variant: Lean, Base: good}); err != nil {
		t.Fatal(err)
	}
	bad := release(t, gz(tarred), zst(tarred), zipBytes(goodEntries), true)
	_, err := Install(context.Background(), d, netguard.Client(true), InstallOptions{Variant: Lean, Base: bad})
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want checksum error, got %v", err)
	}
	if _, ok := Installed(d); !ok {
		t.Error("a failed update must leave the working engine alone")
	}
}

func TestUntarBlocksEscapes(t *testing.T) {
	for _, entries := range [][]entry{
		{{name: "../evil", body: "x"}},
		{{name: "ok", body: "x"}, {name: "link", link: "../../etc/passwd"}},
	} {
		dest := t.TempDir()
		if err := untar(bytes.NewReader(tarBytes(entries)), dest, Full); err == nil {
			t.Errorf("%+v should be refused", entries)
		}
	}
}

func TestOfflineInstallRefused(t *testing.T) {
	d, _ := drive.Find(t.TempDir())
	_, err := Install(context.Background(), d, netguard.Client(false), InstallOptions{Variant: Lean})
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("want offline error, got %v", err)
	}
}

func TestDownloadResumes(t *testing.T) {
	body := []byte("0123456789")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f", fixedTime, bytes.NewReader(body))
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "f.part")
	os.WriteFile(p, body[:4], 0o644)
	if err := Download(context.Background(), netguard.Client(true), srv.URL, p, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(body) {
		t.Errorf("got %q", got)
	}
}

var fixedTime = mustTime()

func mustTime() (t time.Time) { return time.Unix(1700000000, 0) }
