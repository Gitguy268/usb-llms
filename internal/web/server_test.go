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
)

// liveDrive makes the drive believe an engine is running, backed by a fake.
func liveDrive(t *testing.T, reply []string) *drive.Drive {
	t.Helper()
	d, err := drive.Find(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d.SaveConfig(drive.Config{Mode: "offline"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"version":"test"}`)
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"smol","size":1000}]}`)
		case "/api/chat":
			for _, part := range reply {
				raw, _ := json.Marshal(map[string]any{"message": map[string]string{"content": part}})
				fmt.Fprintln(w, string(raw))
			}
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	host, _ := os.Hostname()
	raw, _ := json.Marshal(engine.State{PID: os.Getpid(), Port: port, Online: false, Hostname: host})
	if err := os.WriteFile(filepath.Join(d.Run(), "engine.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func do(h http.Handler, method, target, body string, mod func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Host = "127.0.0.1:11436"
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGuardBlocksOtherSites(t *testing.T) {
	d := liveDrive(t, nil)
	h := (&Server{Drive: d}).Handler()
	if got := do(h, "GET", "/api/status", "", nil).Code; got != 200 {
		t.Errorf("localhost should work, got %d", got)
	}
	rebinding := do(h, "GET", "/api/chats", "", func(r *http.Request) { r.Host = "evil.example:11436" })
	if rebinding.Code != 403 {
		t.Errorf("DNS rebinding should be blocked, got %d", rebinding.Code)
	}
	cross := do(h, "POST", "/api/mode", `{"online":true}`, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") })
	if cross.Code != 403 {
		t.Errorf("cross-site post should be blocked, got %d", cross.Code)
	}
	if d.Online() {
		t.Error("the blocked post must not change the mode")
	}
	same := do(h, "POST", "/api/mode", `{"online":false}`, func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:11436") })
	if same.Code != 200 {
		t.Errorf("same-origin post should work, got %d: %s", same.Code, same.Body)
	}
}

func TestLANRequiresToken(t *testing.T) {
	d := liveDrive(t, nil)
	h := (&Server{Drive: d, Token: "tok"}).Handler()
	lan := func(r *http.Request) { r.Host = "192.168.1.5:11436" }
	if got := do(h, "GET", "/api/status", "", lan).Code; got != 401 {
		t.Errorf("no token: %d", got)
	}
	if got := do(h, "GET", "/api/status?token=wrong", "", lan).Code; got != 401 {
		t.Errorf("wrong token: %d", got)
	}
	ok := do(h, "GET", "/?token=tok", "", lan)
	if ok.Code != 200 {
		t.Fatalf("right token: %d", ok.Code)
	}
	cookie := ok.Result().Cookies()
	if len(cookie) != 1 || !cookie[0].HttpOnly || cookie[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie: %+v", cookie)
	}
	again := do(h, "GET", "/api/status", "", func(r *http.Request) { lan(r); r.AddCookie(cookie[0]) })
	if again.Code != 200 {
		t.Errorf("cookie should be enough: %d", again.Code)
	}
}

func TestPageHasNoOutsideReferences(t *testing.T) {
	for _, name := range []string{"index.html", "style.css", "app.js"} {
		raw, err := uiFiles.ReadFile("ui/" + name)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"https://", "http://"} {
			if strings.Contains(string(raw), bad) {
				t.Errorf("%s refers to the network; the page must work offline", name)
			}
		}
	}
	w := do((&Server{Drive: liveDrive(t, nil)}).Handler(), "GET", "/", "", nil)
	if !strings.Contains(w.Body.String(), "USB LLMS") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Error("page or CSP missing")
	}
}

func TestChatStreamsAndSaves(t *testing.T) {
	d := liveDrive(t, []string{"Hel", "lo"})
	h := (&Server{Drive: d}).Handler()
	w := do(h, "POST", "/api/chat", `{"model":"smol","content":"hi there"}`, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"delta":"Hel"`) || !strings.Contains(w.Body.String(), `"done":true`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	list, _ := chat.List(d)
	if len(list) != 1 || list[0].Title != "hi there" {
		t.Fatalf("%+v", list)
	}
	saved, _ := chat.Load(d, list[0].ID)
	if len(saved.Messages) != 2 || saved.Messages[1].Content != "Hello" {
		t.Errorf("%+v", saved)
	}
	next := do(h, "POST", "/api/chat", fmt.Sprintf(`{"id":%q,"model":"smol","content":"again"}`, list[0].ID), nil)
	if next.Code != 200 {
		t.Fatal(next.Body)
	}
	saved, _ = chat.Load(d, list[0].ID)
	if len(saved.Messages) != 4 {
		t.Errorf("should continue the same chat: %d", len(saved.Messages))
	}
	if got := do(h, "GET", "/api/chats/"+list[0].ID, "", nil).Code; got != 200 {
		t.Errorf("get: %d", got)
	}
	if got := do(h, "DELETE", "/api/chats/"+list[0].ID, "", nil).Code; got != 200 {
		t.Errorf("delete: %d", got)
	}
	if got := do(h, "GET", "/api/chats/..%2f..%2fconfig", "", nil).Code; got == 200 {
		t.Error("path escape should fail")
	}
}

func TestChatNeedsModelAndText(t *testing.T) {
	h := (&Server{Drive: liveDrive(t, nil)}).Handler()
	if got := do(h, "POST", "/api/chat", `{"model":"","content":"x"}`, nil).Code; got != 400 {
		t.Errorf("got %d", got)
	}
	if got := do(h, "POST", "/api/chat", `{"model":"m","content":"  "}`, nil).Code; got != 400 {
		t.Errorf("got %d", got)
	}
}

func TestPullRefusedOffline(t *testing.T) {
	h := (&Server{Drive: liveDrive(t, nil)}).Handler()
	w := do(h, "POST", "/api/pull", `{"spec":"gh:o/r"}`, nil)
	if !strings.Contains(w.Body.String(), "offline") {
		t.Errorf("got %s", w.Body)
	}
}

func TestStatusAndModels(t *testing.T) {
	h := (&Server{Drive: liveDrive(t, nil)}).Handler()
	var s map[string]any
	json.Unmarshal(do(h, "GET", "/api/status", "", nil).Body.Bytes(), &s)
	if s["online"] != false || s["engineRunning"] != true {
		t.Errorf("%v", s)
	}
	if body := do(h, "GET", "/api/models", "", nil).Body.String(); !strings.Contains(body, "smol") {
		t.Error(body)
	}
	if body := do(h, "GET", "/api/catalog", "", nil).Body.String(); !strings.Contains(body, "smollm2-135m") {
		t.Error(body)
	}
}
