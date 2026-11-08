package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Gitguy268/usb-llms/internal/chat"
	"github.com/Gitguy268/usb-llms/internal/drive"
	"github.com/Gitguy268/usb-llms/internal/engine"
	"github.com/Gitguy268/usb-llms/internal/media"
)

// senseDrive fakes an engine whose model has the given capabilities and records chat bodies.
func senseDrive(t *testing.T, caps []string) (*drive.Drive, *[]map[string]any) {
	t.Helper()
	d, err := drive.Find(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d.SaveConfig(drive.Config{Mode: "offline"})
	bodies := &[]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"version":"test"}`)
		case "/api/show":
			json.NewEncoder(w).Encode(map[string]any{"capabilities": caps})
		case "/api/chat":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			*bodies = append(*bodies, in)
			fmt.Fprintln(w, `{"message":{"content":"ok"}}`)
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	host, _ := os.Hostname()
	raw, _ := json.Marshal(engine.State{PID: os.Getpid(), Port: port, Hostname: host})
	os.WriteFile(filepath.Join(d.Run(), "engine.json"), raw, 0o644)
	return d, bodies
}

const pngB64 = "iVBORw0KGgo="
const wavB64 = "UklGRgAAAABXQVZFZm10IA=="

func TestChatSendsPicturesAndAudioToModelsThatTakeThem(t *testing.T) {
	d, bodies := senseDrive(t, []string{"completion", "vision", "audio"})
	h := (&Server{Drive: d}).Handler()
	body := fmt.Sprintf(`{"model":"omni","content":"what is this?","attachments":[{"name":"a.png","mime":"image/png","data":"%s"},{"name":"note","mime":"audio/wav","data":"data:audio/wav;base64,%s"}]}`, pngB64, wavB64)
	w := do(h, "POST", "/api/chat", body, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"done":true`) {
		t.Fatalf("chat: %d %s", w.Code, w.Body)
	}
	msgs := (*bodies)[0]["messages"].([]any)
	images := msgs[0].(map[string]any)["images"].([]any)
	if len(images) != 2 || images[0] != pngB64 || images[1] != wavB64 {
		t.Errorf("engine got images %v", images)
	}
	list, _ := chat.List(d)
	conv, _ := chat.Load(d, list[0].ID)
	if len(conv.Messages[0].Files) != 2 || strings.Contains(string(mustJSON(conv)), pngB64) {
		t.Error("the saved chat should reference files, not hold their bytes")
	}
	// The uploads are served for showing the saved chat again.
	if got := do(h, "GET", "/media/uploads/"+conv.Messages[0].Files[0], "", nil); got.Code != 200 || got.Header().Get("Content-Type") != "image/png" {
		t.Errorf("upload: %d %s", got.Code, got.Header().Get("Content-Type"))
	}
}

func TestChatLeavesPicturesOutForTextModels(t *testing.T) {
	d, bodies := senseDrive(t, []string{"completion"})
	h := (&Server{Drive: d}).Handler()
	w := do(h, "POST", "/api/chat", fmt.Sprintf(`{"model":"smol","content":"hi","attachments":[{"name":"a.png","mime":"image/png","data":"%s"}]}`, pngB64), nil)
	if !strings.Contains(w.Body.String(), "can't see pictures") {
		t.Errorf("expected a note, got %s", w.Body)
	}
	if _, has := (*bodies)[0]["messages"].([]any)[0].(map[string]any)["images"]; has {
		t.Error("a text model must not get the picture")
	}
	// A voice note for a model that doesn't hear needs transcription, which isn't set up here.
	w = do(h, "POST", "/api/chat", fmt.Sprintf(`{"model":"smol","content":"","attachments":[{"name":"n","mime":"audio/wav","data":"%s"}]}`, wavB64), nil)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "couldn't be transcribed") {
		t.Errorf("voice note to a text model: %d %s", w.Code, w.Body)
	}
}

func TestMediaRoutes(t *testing.T) {
	d := liveDrive(t, nil)
	h := (&Server{Drive: d}).Handler()
	o, err := media.Save(d, "3d", []byte("glTF"), ".glb", map[string]any{"prompt": "cube"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := do(h, "GET", "/media/"+o.Rel, "", nil)
	if got.Code != 200 || got.Header().Get("Content-Type") != "model/gltf-binary" || !strings.Contains(got.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("media file: %d %v", got.Code, got.Header())
	}
	if got := do(h, "GET", "/media/..%2Fconfig.json", "", nil).Code; got != 404 {
		t.Errorf("traversal: %d", got)
	}
	page := do(h, "GET", "/", "", nil)
	if csp := page.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "media-src 'self'") || !strings.Contains(csp, "wasm-unsafe-eval") {
		t.Errorf("CSP: %s", csp)
	}
	var status struct {
		Catalog []map[string]any `json:"catalog"`
		Outputs []map[string]any `json:"outputs"`
	}
	json.Unmarshal(do(h, "GET", "/api/media", "", nil).Body.Bytes(), &status)
	if len(status.Catalog) < 6 || len(status.Outputs) != 1 {
		t.Errorf("status: %d catalog, %d outputs", len(status.Catalog), len(status.Outputs))
	}
	mk := do(h, "POST", "/api/media/make", `{"kind":"speech","prompt":"hi"}`, nil)
	if !strings.Contains(mk.Body.String(), "media install speech") {
		t.Errorf("make without an engine should explain setup: %s", mk.Body)
	}
	if do(h, "GET", "/vendor/model-viewer.min.js", "", nil).Code != 200 {
		t.Error("the 3D viewer isn't served")
	}
}

func mustJSON(v any) []byte { raw, _ := json.Marshal(v); return raw }
