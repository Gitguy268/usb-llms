package webui

import (
	"os"
	"strings"
	"testing"

	"github.com/Gitguy268/usb-llms/internal/drive"
)

func env(list []string) map[string]string {
	out := map[string]string{}
	for _, kv := range list {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

func TestEnvOnline(t *testing.T) {
	d, _ := drive.Find(t.TempDir())
	got := env(Env(nil, d, "http://127.0.0.1:11435", "s", true))
	if got["OLLAMA_BASE_URL"] != "http://127.0.0.1:11435" || !strings.HasPrefix(got["DATA_DIR"], d.Root) || !strings.HasPrefix(got["HF_HOME"], d.Root) || !strings.HasPrefix(got["HOME"], d.Root) {
		t.Errorf("%v", got)
	}
	if got["OFFLINE_MODE"] != "" {
		t.Error("online shouldn't set offline mode")
	}
	if got["DO_NOT_TRACK"] != "true" {
		t.Error("telemetry should be off")
	}
}

func TestEnvOffline(t *testing.T) {
	d, _ := drive.Find(t.TempDir())
	got := env(Env([]string{"OPENAI_API_KEY=k", "HTTPS_PROXY=x"}, d, "http://127.0.0.1:11435", "s", false))
	if got["OFFLINE_MODE"] != "true" || got["HF_HUB_OFFLINE"] != "1" || got["ENABLE_OPENAI_API"] != "False" {
		t.Errorf("%v", got)
	}
	if got["OPENAI_API_KEY"] != "" || got["HTTPS_PROXY"] != deadProxy {
		t.Errorf("host values must not pass through offline: %v", got)
	}
}

func TestCommandExplainsMissingInstall(t *testing.T) {
	d, _ := drive.Find(t.TempDir())
	if _, err := Command(t.Context(), d, "", false); err == nil || !strings.Contains(err.Error(), "webui install") {
		t.Errorf("got %v", err)
	}
}

func TestInstallRefusedOffline(t *testing.T) {
	d, _ := drive.Find(t.TempDir())
	d.SaveConfig(drive.Config{Mode: "offline"})
	if err := Install(t.Context(), d, nil, os.Stdout); err == nil || !strings.Contains(err.Error(), "mode online") {
		t.Errorf("got %v", err)
	}
}

func TestSecretIsStable(t *testing.T) {
	d, _ := drive.Find(t.TempDir())
	a, err := Secret(d)
	if err != nil || len(a) != 64 {
		t.Fatal(a, err)
	}
	if b, _ := Secret(d); b != a {
		t.Error("the key must persist or every login breaks")
	}
}

func TestUVAsset(t *testing.T) {
	for _, p := range [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}} {
		if _, err := UVAsset(p[0], p[1]); err != nil {
			t.Error(err)
		}
	}
	if _, err := UVAsset("plan9", "386"); err == nil {
		t.Error("should fail")
	}
}
