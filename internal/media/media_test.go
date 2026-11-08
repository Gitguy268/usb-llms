package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gitguy268/usb-llms/internal/drive"
)

func testDrive(t *testing.T, mode string) *drive.Drive {
	t.Helper()
	d, err := drive.Find(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SaveConfig(drive.Config{Mode: mode}); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCatalogIsCompleteAndMatchesLvW(t *testing.T) {
	defaults := map[string]int{}
	for _, e := range Catalog(nil) {
		if e.ID == "" || e.Name == "" || e.Licence == "" || len(e.Kinds) == 0 {
			t.Errorf("%q is missing fields", e.ID)
		}
		for _, k := range e.Kinds {
			found := false
			for _, known := range Kinds {
				found = found || k == known
			}
			if !found {
				t.Errorf("%s: unknown kind %q", e.ID, k)
			}
			if e.Default {
				defaults[k]++
			}
		}
		switch e.Engine {
		case "sdcpp":
			if len(e.Components) == 0 {
				t.Errorf("%s has no components", e.ID)
			}
			for _, c := range e.Components {
				if _, _, err := ParseComponent(c); err != nil {
					t.Error(err)
				}
			}
		case "ollama":
			if e.Ollama == "" {
				t.Errorf("%s has no Ollama name", e.ID)
			}
		case "triposr", "acestep":
		default:
			t.Errorf("%s: unknown engine %q", e.ID, e.Engine)
		}
	}
	for k, n := range defaults {
		if n != 1 {
			t.Errorf("%d defaults for %s", n, k)
		}
	}
}

func TestSDAssetPattern(t *testing.T) {
	names := []string{
		"sd-master-7bcd4ce-bin-Darwin-macOS-26.7.2-arm64.zip",
		"sd-master-7bcd4ce-bin-Linux-Ubuntu-26.04-x86_64.zip",
		"sd-master-7bcd4ce-bin-Linux-Ubuntu-26.04-x86_64-vulkan.zip",
		"sd-master-7bcd4ce-bin-win-cpu-x64.zip",
		"sd-master-7bcd4ce-bin-win-cuda12-x64.zip",
		"cudart-sd-bin-win-cu12-x64.zip",
	}
	cases := []struct{ goos, arch, variant, want string }{
		{"darwin", "arm64", "", names[0]}, {"darwin", "amd64", "", names[0]},
		{"linux", "amd64", "", names[1]}, {"linux", "amd64", "vulkan", names[2]},
		{"windows", "amd64", "", names[3]}, {"windows", "amd64", "cuda12", names[4]},
	}
	for _, c := range cases {
		re, err := SDAssetPattern(c.goos, c.arch, c.variant)
		if err != nil {
			t.Fatal(err)
		}
		hits := []string{}
		for _, n := range names {
			if re.MatchString(n) {
				hits = append(hits, n)
			}
		}
		if len(hits) != 1 || hits[0] != c.want {
			t.Errorf("%s/%s/%s matched %v", c.goos, c.arch, c.variant, hits)
		}
	}
	for _, p := range [][2]string{{"linux", "arm64"}, {"windows", "arm64"}} {
		if _, err := SDAssetPattern(p[0], p[1], ""); err == nil {
			t.Errorf("%v should have no build", p)
		}
	}
}

func TestSDArgsAndBody(t *testing.T) {
	wan, _ := Find(nil, "wan2.1-t2v-1.3b")
	m := Installed{ID: wan.ID, Entry: wan, Files: map[string]string{"diffusion-model": "/d.st", "vae": "/v.st", "t5xxl": "/t.gguf"}}
	args := strings.Join(SDArgs(m, 11438, "linux"), " ")
	for _, want := range []string{"--listen-port 11438", "--diffusion-model /d.st", "--t5xxl /t.gguf", "--offload-to-cpu"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	sd15, _ := Find(nil, "sd15")
	mac := strings.Join(SDArgs(Installed{Entry: sd15, Files: map[string]string{"model": "/m.gguf"}}, 1, "darwin"), " ")
	if !strings.Contains(mac, "-m /m.gguf") || !strings.Contains(mac, "--backend diffusion=cpu") {
		t.Errorf("mac args: %s", mac)
	}
	seed := int64(5)
	b := SDBody("video", Request{Prompt: "waves", Seed: &seed, Image: []byte{1, 2}}, wan)
	if b["video_frames"] != 17 || b["output_format"] != "webm" || b["seed"] != int64(5) || b["init_image"] != "AQI=" {
		t.Errorf("video body: %v", b)
	}
	img := SDBody("image", Request{Prompt: "x", Steps: 4}, sd15)
	sp := img["sample_params"].(map[string]any)
	if sp["sample_steps"] != 4 || sp["sample_method"] != "euler_a" || img["width"] != 512 {
		t.Errorf("image body: %v", img)
	}
}

func TestSaveListAndFileSafety(t *testing.T) {
	d := testDrive(t, "offline")
	text := "hello"
	o, err := Save(d, "transcribe", []byte(text), ".txt", map[string]any{"model": "x"}, &text)
	if err != nil {
		t.Fatal(err)
	}
	list := List(d, 10)
	if len(list) != 1 || list[0].Rel != o.Rel || list[0].Text == nil || *list[0].Text != "hello" {
		t.Fatalf("list: %+v", list)
	}
	if p, ok := File(d, o.Rel); !ok || p != o.Path {
		t.Error("saved file not found")
	}
	for _, bad := range []string{"../data/config.json", "/etc/passwd", `..\x`, ""} {
		if _, ok := File(d, bad); ok {
			t.Errorf("%q escaped the media folder", bad)
		}
	}
	if !Remove(d, o.Rel) || len(List(d, 10)) != 0 {
		t.Error("remove failed")
	}
}

func TestPullDownloadsComponentsWithChecksums(t *testing.T) {
	d := testDrive(t, "online")
	files := map[string][]byte{"dir/big.gguf": []byte("GGUF-big-weights"), "vae.st": []byte("vae")}
	hf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/models/me/tiny/tree/main"):
			dir := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/models/me/tiny/tree/main"), "/")
			out := []map[string]any{}
			for p, b := range files {
				if filepath.ToSlash(filepath.Dir(p)) == dir || (dir == "" && !strings.Contains(p, "/")) {
					sum := sha256.Sum256(b)
					out = append(out, map[string]any{"type": "file", "path": p, "size": len(b), "lfs": map[string]any{"oid": hex.EncodeToString(sum[:]), "size": len(b)}})
				}
			}
			json.NewEncoder(w).Encode(out)
		case strings.HasPrefix(r.URL.Path, "/me/tiny/resolve/main/"):
			w.Write(files[strings.TrimPrefix(r.URL.Path, "/me/tiny/resolve/main/")])
		default:
			http.NotFound(w, r)
		}
	}))
	defer hf.Close()
	old := HFEndpoint
	HFEndpoint = hf.URL
	defer func() { HFEndpoint = old }()
	// Pretend sd-server is already on the drive so Pull doesn't fetch it.
	bin := filepath.Join(d.Engine(), "sd", exe("sd-server"))
	os.MkdirAll(filepath.Dir(bin), 0o755)
	os.WriteFile(bin, []byte("x"), 0o755)
	own := `{"entries":[{"id":"tiny","kinds":["image"],"engine":"sdcpp","name":"Tiny","licence":"MIT","components":{"model":"hf:me/tiny/dir/big.gguf","vae":"hf:me/tiny/vae.st"}}]}`
	os.WriteFile(filepath.Join(d.Data(), "media-catalog.json"), []byte(own), 0o644)
	if err := Pull(context.Background(), d, "tiny", nil, nil); err != nil {
		t.Fatal(err)
	}
	m, ok := LoadInstalled(d, "tiny")
	if !ok {
		t.Fatal("not installed")
	}
	if raw, _ := os.ReadFile(m.Files["model"]); string(raw) != "GGUF-big-weights" {
		t.Errorf("model file: %q", raw)
	}
	files["vae.st"] = []byte("tampered")
	os.RemoveAll(filepath.Join(d.MediaModels(), "tiny"))
	// The listing still has the old checksum, so the tampered file must be refused.
	hf.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			sum := sha256.Sum256([]byte("vae"))
			json.NewEncoder(w).Encode([]map[string]any{{"type": "file", "path": "vae.st", "size": 8, "lfs": map[string]any{"oid": hex.EncodeToString(sum[:]), "size": 8}}, {"type": "file", "path": "dir/big.gguf", "size": 16}})
			return
		}
		w.Write(files[strings.TrimPrefix(r.URL.Path, "/me/tiny/resolve/main/")])
	})
	if err := Pull(context.Background(), d, "tiny", nil, nil); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("tampered file should fail its checksum, got %v", err)
	}
}

func TestPullRefusedOffline(t *testing.T) {
	d := testDrive(t, "offline")
	if err := Pull(context.Background(), d, "sd15", nil, nil); err == nil {
		t.Error("offline pull should be refused")
	}
}

func TestSpeechTranscribeAndRemoteFallback(t *testing.T) {
	var got []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path+" "+r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/audio/speech":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			if in["response_format"] != "wav" || in["voice"] != "alloy" {
				http.Error(w, "bad request", 400)
				return
			}
			w.Write([]byte("RIFF\x00\x00\x00\x00WAVEfmt "))
		case "/v1/audio/transcriptions":
			if err := r.ParseMultipartForm(1 << 20); err != nil || r.FormValue("model") != "whisper-1" {
				http.Error(w, "bad form", 400)
				return
			}
			fmt.Fprint(w, `{"text":" hello there "}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	d := testDrive(t, "online")
	t.Setenv("TEST_MEDIA_KEY", "sk-test")
	cfg, _ := d.LoadConfig()
	cfg.Remotes = []drive.Remote{{Name: "fake", URL: api.URL + "/v1", KeyEnv: "TEST_MEDIA_KEY"}}
	cfg.Media = drive.Media{API: "fake", Speech: "tts-1", Voice: "alloy", Transcribe: "whisper-1"}
	d.SaveConfig(cfg)
	ctx := context.Background()
	o, err := Make(ctx, d, Request{Kind: "speech", Prompt: "hi"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(o.Path); !IsWav(raw) || o.Meta["model"] != "api:tts-1" {
		t.Errorf("speech output: %+v", o)
	}
	o, err = Make(ctx, d, Request{Kind: "transcribe", Audio: []byte("RIFF....WAVE")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.Text == nil || *o.Text != "hello there" {
		t.Errorf("transcript: %+v", o.Text)
	}
	if !strings.Contains(got[0], "Bearer sk-test") {
		t.Errorf("key not sent: %v", got)
	}
	// Offline, the remote is never used.
	cfg.Mode = "offline"
	d.SaveConfig(cfg)
	if _, err := Make(ctx, d, Request{Kind: "speech", Prompt: "hi"}, nil); err == nil || !strings.Contains(err.Error(), "media install speech") {
		t.Errorf("offline should explain how to set speech up, got %v", err)
	}
}

func TestSidecarEnvKeepsDataOnTheDrive(t *testing.T) {
	d := testDrive(t, "offline")
	env := strings.Join(SidecarEnv([]string{"HF_HOME=/host/hf", "HTTPS_PROXY=http://corp", "PATH=/bin"}, d, false), "\n")
	for _, want := range []string{"HF_HOME=" + filepath.Join(d.Sidecars(), "hf"), "HF_HUB_OFFLINE=1", "HTTPS_PROXY=" + deadProxy, "PATH=/bin"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	if strings.Contains(env, "/host/hf") || strings.Contains(env, "corp") {
		t.Error("host settings leaked into the sidecar")
	}
}
