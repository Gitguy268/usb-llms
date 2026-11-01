package lvw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gitguy268/usb-llms/internal/drive"
)

func TestSyncKeepsSettingsAndUpdatesURL(t *testing.T) {
	d, _ := drive.Find(t.TempDir())
	os.MkdirAll(d.LvwHome(), 0o755)
	path := filepath.Join(d.LvwHome(), "config.json")
	os.WriteFile(path, []byte(`{"permissions":"auto","ollamaUrl":"http://old:1"}`), 0o644)
	if err := Sync(d, "http://127.0.0.1:11440", "smol"); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	raw, _ := os.ReadFile(path)
	json.Unmarshal(raw, &got)
	if got["ollamaUrl"] != "http://127.0.0.1:11440" || got["permissions"] != "auto" || got["model"] != "ollama:smol" {
		t.Errorf("%v", got)
	}
	if err := Sync(d, "http://127.0.0.1:11435", "other"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	json.Unmarshal(raw, &got)
	if got["model"] != "ollama:smol" {
		t.Error("a chosen model must not be replaced", got["model"])
	}
}

func TestEnv(t *testing.T) {
	d, _ := drive.Find(t.TempDir())
	on := strings.Join(Env([]string{"LVW_HOME=/x", "OLLAMA_HOST=y", "HTTPS_PROXY=z"}, d, "http://127.0.0.1:1", true), "\n")
	if strings.Contains(on, "/x") || !strings.Contains(on, "LVW_HOME="+d.LvwHome()) || strings.Contains(on, "HTTPS_PROXY") {
		t.Error(on)
	}
	off := strings.Join(Env(nil, d, "http://127.0.0.1:1", false), "\n")
	if !strings.Contains(off, "HTTPS_PROXY="+deadProxy) || !strings.Contains(off, "127.0.0.1") {
		t.Error(off)
	}
}

func TestInstallCopiesBinary(t *testing.T) {
	d, _ := drive.Find(t.TempDir())
	src := filepath.Join(t.TempDir(), "lvw")
	os.WriteFile(src, []byte("bin"), 0o755)
	dest, err := Install(d, src)
	if err != nil {
		t.Fatal(err)
	}
	if found, err := Find(d); err != nil || found != dest {
		t.Errorf("%q %v", found, err)
	}
}
