package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gitguy268/usb-llms/internal/drive"
	"github.com/Gitguy268/usb-llms/internal/netguard"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Source
	}{
		{"hf:o/r", Source{Kind: "hf", Repo: "o/r"}},
		{"hf:o/r:Q4_K_M", Source{Kind: "hf", Repo: "o/r", Pattern: "Q4_K_M"}},
		{"o/r", Source{Kind: "hf", Repo: "o/r"}},
		{"hf.co/o/r:Q8_0", Source{Kind: "hf", Repo: "o/r", Pattern: "Q8_0"}},
		{"gh:modelmaker/aimodel1", Source{Kind: "gh", Repo: "modelmaker/aimodel1"}},
		{"gh:o/r@v1:*.gguf", Source{Kind: "gh", Repo: "o/r", Ref: "v1", Pattern: "*.gguf"}},
		{"https://github.com/modelmaker/aimodel1", Source{Kind: "gh", Repo: "modelmaker/aimodel1"}},
		{"https://github.com/o/r.git", Source{Kind: "gh", Repo: "o/r"}},
		{"https://github.com/o/r/releases/tag/v2", Source{Kind: "gh", Repo: "o/r", Ref: "v2"}},
		{"https://github.com/o/r/releases/download/v2/m.gguf", Source{Kind: "url", URL: "https://github.com/o/r/releases/download/v2/m.gguf"}},
		{"https://huggingface.co/o/r", Source{Kind: "hf", Repo: "o/r"}},
		{"https://huggingface.co/o/r/resolve/main/m.gguf", Source{Kind: "hf", Repo: "o/r", Ref: "main", Pattern: "m.gguf"}},
		{"https://example.com/m.gguf", Source{Kind: "url", URL: "https://example.com/m.gguf"}},
		{"ollama:llama3.2", Source{Kind: "ollama", Name: "llama3.2"}},
		{"llama3.2:1b", Source{Kind: "ollama", Name: "llama3.2:1b"}},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
		} else if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "ollama:", "gl:a/b", "hf:justone", "gh:a/b/c", "missing-file.gguf"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestEngineRef(t *testing.T) {
	cases := map[string]string{
		"hf:o/r":        "hf.co/o/r",
		"hf:o/r:Q4_K_M": "hf.co/o/r:Q4_K_M",
		"ollama:phi3":   "phi3",
		"hf:o/r:*.gguf": "",
		"hf:o/r:a.gguf": "",
		"hf:o/r@dev:Q4": "",
		"gh:o/r":        "",
	}
	for spec, want := range cases {
		s, err := Parse(spec)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := s.EngineRef()
		if got != want || ok != (want != "") {
			t.Errorf("%s: got %q %v", spec, got, ok)
		}
	}
}

func TestLocalName(t *testing.T) {
	cases := [][3]string{
		{"aimodel1", "aimodel1-Q4_K_M.gguf", "aimodel1:q4_k_m"},
		{"Some-Model-GGUF", "Some-Model.Q8_0.gguf", "some-model:q8_0"},
		{"model.gguf", "model.gguf", "model"},
		{"My Model!.gguf", "My Model!.gguf", "my-model"},
		{"SmolLM2-135M-Instruct-Q4_K_M.gguf", "SmolLM2-135M-Instruct-Q4_K_M.gguf", "smollm2-135m-instruct:q4_k_m"},
	}
	for _, c := range cases {
		if got := LocalName(c[0], c[1]); got != c[2] {
			t.Errorf("LocalName(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestChoose(t *testing.T) {
	assets := []Asset{{Name: "README.md"}, {Name: "mmproj-x.gguf"}, {Name: "m-Q8_0.gguf"}, {Name: "m-Q4_K_M.gguf"}}
	if a, _ := Choose(assets, ""); a.Name != "m-Q4_K_M.gguf" {
		t.Error("should prefer Q4_K_M", a)
	}
	if a, _ := Choose(assets, "Q8_0"); a.Name != "m-Q8_0.gguf" {
		t.Error("pattern", a)
	}
	if a, _ := Choose(assets, "*Q8*"); a.Name != "m-Q8_0.gguf" {
		t.Error("glob", a)
	}
	if _, err := Choose(assets, "Q2"); err == nil || !strings.Contains(err.Error(), "m-Q8_0.gguf") {
		t.Error("should list what exists", err)
	}
	if _, err := Choose([]Asset{{Name: "README.md"}}, ""); err == nil {
		t.Error("no gguf should fail")
	}
	if _, err := Choose([]Asset{{Name: "m-00001-of-00002.gguf"}}, ""); err == nil {
		t.Error("split files should fail clearly")
	}
	if a, _ := Choose([]Asset{{Name: "only.gguf"}}, ""); a.Name != "only.gguf" {
		t.Error("single file", a)
	}
}

func TestCatalogIsComplete(t *testing.T) {
	seen := map[string]bool{}
	defaults := 0
	for _, e := range Catalog() {
		if seen[e.ID] {
			t.Errorf("duplicate id %s", e.ID)
		}
		seen[e.ID] = true
		if e.Default {
			defaults++
			if e.Label != "standard" {
				t.Errorf("the default model must be standard, not %s", e.Label)
			}
		}
		s, err := Parse(e.Spec)
		if err != nil {
			t.Errorf("%s: %v", e.ID, err)
		}
		if _, ok := s.EngineRef(); !ok {
			t.Errorf("%s should pull natively", e.ID)
		}
		if e.SizeGB <= 0 || e.RAMGB <= 0 || e.Licence == "" || e.Note == "" {
			t.Errorf("%s is missing facts", e.ID)
		}
	}
	if defaults != 1 {
		t.Errorf("want one default, got %d", defaults)
	}
}

// fakeEngine records what the importer sends.
type fakeEngine struct {
	blobs   map[string][]byte
	created map[string]any
	pulled  []string
	deleted []string
	copied  []map[string]string
}

func newFakeEngine(t *testing.T) (*fakeEngine, string) {
	f := &fakeEngine{blobs: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/blobs/"):
			digest := strings.TrimPrefix(r.URL.Path, "/api/blobs/")
			if r.Method == "HEAD" {
				if _, ok := f.blobs[digest]; !ok {
					w.WriteHeader(404)
				}
				return
			}
			f.blobs[digest], _ = io.ReadAll(r.Body)
			w.WriteHeader(201)
		case r.URL.Path == "/api/create":
			json.NewDecoder(r.Body).Decode(&f.created)
			fmt.Fprintln(w, `{"status":"success"}`)
		case r.URL.Path == "/api/pull":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			f.pulled = append(f.pulled, in["model"].(string))
			if strings.Contains(in["model"].(string), "missing") {
				fmt.Fprintln(w, `{"error":"pull model manifest: file does not exist"}`)
				return
			}
			fmt.Fprintln(w, `{"status":"pulling","completed":1,"total":2}`)
			fmt.Fprintln(w, `{"status":"success"}`)
		case r.URL.Path == "/api/copy":
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			f.copied = append(f.copied, in)
		case r.URL.Path == "/api/delete":
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			f.deleted = append(f.deleted, in["model"])
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func onlineDrive(t *testing.T) *drive.Drive {
	d, err := drive.Find(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SaveConfig(drive.Config{Mode: "online"}); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPullFromGitHubReleaseImportsAndCleansUp(t *testing.T) {
	gguf := append([]byte("GGUF"), make([]byte, 100)...)
	var host string
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/modelmaker/aimodel1/releases/latest":
			json.NewEncoder(w).Encode(map[string]any{"assets": []map[string]any{
				{"name": "notes.txt", "size": 1, "browser_download_url": host + "/notes"},
				{"name": "aimodel1-Q4_K_M.gguf", "size": len(gguf), "browser_download_url": host + "/dl"},
			}})
		case "/dl":
			w.Write(gguf)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()
	host = gh.URL
	f, engineURL := newFakeEngine(t)
	d := onlineDrive(t)
	p := New(d, engineURL)
	p.GitHubAPI = gh.URL
	name, err := p.Pull(context.Background(), "gh:modelmaker/aimodel1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if name != "aimodel1:q4_k_m" {
		t.Errorf("name = %q", name)
	}
	if f.created["model"] != name || len(f.blobs) != 1 {
		t.Errorf("create: %+v, %d blobs", f.created, len(f.blobs))
	}
	left, _ := filepath.Glob(filepath.Join(d.Tmp(), "*"))
	if len(left) != 0 {
		t.Errorf("the download should be removed after import: %v", left)
	}
}

func TestPullChecksThatDownloadIsGGUF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>not a model</html>") }))
	defer srv.Close()
	_, engineURL := newFakeEngine(t)
	d := onlineDrive(t)
	_, err := New(d, engineURL).Pull(context.Background(), srv.URL+"/m.gguf", "", nil)
	if err == nil || !strings.Contains(err.Error(), "isn't a GGUF") {
		t.Errorf("got %v", err)
	}
}

func TestPullChecksSHA(t *testing.T) {
	gguf := append([]byte("GGUF"), make([]byte, 10)...)
	var host string
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dl" {
			w.Write(gguf)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"assets": []map[string]any{{"name": "m.gguf", "size": len(gguf), "browser_download_url": host + "/dl", "digest": "sha256:" + strings.Repeat("a", 64)}}})
	}))
	defer gh.Close()
	host = gh.URL
	_, engineURL := newFakeEngine(t)
	p := New(onlineDrive(t), engineURL)
	p.GitHubAPI = gh.URL
	if _, err := p.Pull(context.Background(), "gh:o/r", "", nil); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("got %v", err)
	}
}

func TestPullChecksFreeSpace(t *testing.T) {
	var host string
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"assets": []map[string]any{{"name": "m.gguf", "size": 5_000_000_000, "browser_download_url": host + "/dl"}}})
	}))
	defer gh.Close()
	host = gh.URL
	_, engineURL := newFakeEngine(t)
	p := New(onlineDrive(t), engineURL)
	p.GitHubAPI = gh.URL
	p.Free = func(string) (uint64, error) { return 1_000_000_000, nil }
	if _, err := p.Pull(context.Background(), "gh:o/r", "", nil); err == nil || !strings.Contains(err.Error(), "free") {
		t.Errorf("got %v", err)
	}
}

func TestPullHuggingFaceGoesThroughEngine(t *testing.T) {
	f, engineURL := newFakeEngine(t)
	p := New(onlineDrive(t), engineURL)
	got, err := p.Pull(context.Background(), "smollm2-135m", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hf.co/bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M" || len(f.pulled) != 1 {
		t.Errorf("got %q, pulled %v", got, f.pulled)
	}
	if _, err := p.Pull(context.Background(), "hf:o/missing", "", nil); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("engine errors should surface: %v", err)
	}
}

func TestPullWithNameRenames(t *testing.T) {
	f, engineURL := newFakeEngine(t)
	got, err := New(onlineDrive(t), engineURL).Pull(context.Background(), "hf:o/r:Q4", "short", nil)
	if err != nil || got != "short" {
		t.Fatal(got, err)
	}
	if len(f.copied) != 1 || f.copied[0]["destination"] != "short" || len(f.deleted) != 1 {
		t.Errorf("%+v %v", f.copied, f.deleted)
	}
}

func TestOfflineBlocksDownloadsButNotLocalFiles(t *testing.T) {
	f, engineURL := newFakeEngine(t)
	d := onlineDrive(t)
	d.SaveConfig(drive.Config{Mode: "offline"})
	p := New(d, engineURL)
	for _, spec := range []string{"smollm2-135m", "gh:o/r", "https://example.com/m.gguf", "ollama:phi3"} {
		if _, err := p.Pull(context.Background(), spec, "", nil); !errors.Is(err, netguard.ErrOffline) {
			t.Errorf("%s: want ErrOffline, got %v", spec, err)
		}
	}
	file := filepath.Join(t.TempDir(), "mine.gguf")
	os.WriteFile(file, append([]byte("GGUF"), 1, 2, 3), 0o644)
	name, err := p.Pull(context.Background(), file, "", nil)
	if err != nil || name != "mine" {
		t.Fatalf("local import should work offline: %q %v", name, err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Error("a file the user pointed at must not be deleted")
	}
	if f.created["model"] != "mine" {
		t.Error(f.created)
	}
}
