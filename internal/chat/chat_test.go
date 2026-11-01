package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Gitguy268/usb-llms/internal/drive"
)

func TestStreamFromEngine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"message":{"content":"Hel"}}`)
		fmt.Fprintln(w, `{"message":{"content":"lo"},"done":true}`)
	}))
	defer srv.Close()
	d, _ := drive.Find(t.TempDir())
	got := ""
	err := Stream(context.Background(), d, srv.URL, "m", []Message{{Role: "user", Content: "hi"}}, func(s string) { got += s })
	if err != nil || got != "Hello" {
		t.Errorf("%q %v", got, err)
	}
}

func TestEngineErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprintln(w, `{"error":"model not found"}`)
	}))
	defer srv.Close()
	d, _ := drive.Find(t.TempDir())
	err := Stream(context.Background(), d, srv.URL, "m", nil, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Errorf("got %v", err)
	}
}

func remoteDrive(t *testing.T, url string, online bool) *drive.Drive {
	d, _ := drive.Find(t.TempDir())
	mode := "offline"
	if online {
		mode = "online"
	}
	d.SaveConfig(drive.Config{Mode: mode, Remotes: []drive.Remote{{Name: "svc", URL: url, KeyEnv: "TEST_REMOTE_KEY"}}})
	return d
}

func TestRemoteStreamUsesKeyFromEnvOnly(t *testing.T) {
	var auth, model string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		model, _ = in["model"].(string)
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"Hi"}}]}`)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer srv.Close()
	d := remoteDrive(t, srv.URL, true)
	t.Setenv("TEST_REMOTE_KEY", "secret-value")
	got := ""
	err := Stream(context.Background(), d, "", "remote/svc/org/model-1", nil, func(s string) { got += s })
	if err != nil || got != "Hi" {
		t.Fatalf("%q %v", got, err)
	}
	if auth != "Bearer secret-value" || model != "org/model-1" {
		t.Errorf("auth %q model %q", auth, model)
	}
	raw, _ := os.ReadFile(d.ConfigPath())
	if strings.Contains(string(raw), "secret-value") {
		t.Error("the key must never reach the drive")
	}
}

func TestRemoteNeedsKeyAndOnline(t *testing.T) {
	d := remoteDrive(t, "https://api.example.com/v1", true)
	t.Setenv("TEST_REMOTE_KEY", "")
	err := Stream(context.Background(), d, "", "remote/svc/m", nil, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "TEST_REMOTE_KEY") {
		t.Errorf("got %v", err)
	}
	d = remoteDrive(t, "https://api.example.com/v1", false)
	t.Setenv("TEST_REMOTE_KEY", "k")
	err = Stream(context.Background(), d, "", "remote/svc/m", nil, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("offline must block remotes: %v", err)
	}
}

func TestKeyNeverSentInClear(t *testing.T) {
	if CheckRemoteURL("http://api.example.com/v1", true) == nil {
		t.Error("plain http with a key to another machine must fail")
	}
	if err := CheckRemoteURL("http://127.0.0.1:8000/v1", true); err != nil {
		t.Error("loopback is fine", err)
	}
	if err := CheckRemoteURL("https://api.example.com/v1", true); err != nil {
		t.Error(err)
	}
	if CheckRemoteURL("ftp://x", false) == nil {
		t.Error("only http(s)")
	}
}

func TestConversationStore(t *testing.T) {
	d, _ := drive.Find(t.TempDir())
	c := Conversation{Model: "m", Messages: []Message{{Role: "user", Content: strings.Repeat("word ", 30)}}}
	if err := Save(d, &c); err != nil {
		t.Fatal(err)
	}
	if c.ID == "" || !strings.HasSuffix(c.Title, "…") {
		t.Errorf("%+v", c)
	}
	got, err := Load(d, c.ID)
	if err != nil || len(got.Messages) != 1 {
		t.Fatal(got, err)
	}
	list, _ := List(d)
	if len(list) != 1 || list[0].Messages != nil {
		t.Errorf("%+v", list)
	}
	for _, bad := range []string{"../config", "a/b", `a\b`, ""} {
		if _, err := Load(d, bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	if err := Delete(d, c.ID); err != nil {
		t.Fatal(err)
	}
}
