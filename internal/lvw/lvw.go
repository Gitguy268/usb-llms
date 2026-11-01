// Package lvw runs LvW-harness against the drive's engine, keeping its data on the drive.
package lvw

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Gitguy268/usb-llms/internal/drive"
)

const deadProxy = "http://127.0.0.1:9"

// BinaryPath is where `usb-llms lvw install` puts the binary for this machine.
func BinaryPath(d *drive.Drive) string {
	name := "lvw-" + drive.Platform()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(d.Root, "bin", name)
}

// Find prefers the copy on the drive, then one on PATH.
func Find(d *drive.Drive) (string, error) {
	if p := BinaryPath(d); fileExists(p) {
		return p, nil
	}
	if p, err := exec.LookPath("lvw"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("LvW-harness isn't on this drive or on PATH; run `usb-llms lvw install <path to lvw binary>`")
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// Install copies an lvw binary onto the drive.
func Install(d *drive.Drive, from string) (string, error) {
	if from == "" {
		p, err := exec.LookPath("lvw")
		if err != nil {
			return "", fmt.Errorf("no lvw on PATH; pass the path to a built binary")
		}
		from = p
	}
	src, err := os.Open(from)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dest := BinaryPath(d)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	out, err := os.OpenFile(dest+".tmp", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return dest, os.Rename(dest+".tmp", dest)
}

// Env points LvW at the drive. Offline, a dead proxy also stops its web tools and cloud providers.
func Env(base []string, d *drive.Drive, engineURL string, online bool) []string {
	out := make([]string, 0, len(base)+6)
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(key) {
		case "LVW_HOME", "OLLAMA_HOST", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY":
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "LVW_HOME="+d.LvwHome(), "OLLAMA_HOST="+engineURL)
	if !online {
		out = append(out, "HTTP_PROXY="+deadProxy, "HTTPS_PROXY="+deadProxy, "ALL_PROXY="+deadProxy, "NO_PROXY=localhost,127.0.0.1,::1")
	}
	return out
}

// Sync writes the engine URL into LvW's config, because a saved value beats OLLAMA_HOST and
// the port can change between machines. With no default model yet, it picks the given one.
func Sync(d *drive.Drive, engineURL, model string) error {
	if err := os.MkdirAll(d.LvwHome(), 0o755); err != nil {
		return err
	}
	path := filepath.Join(d.LvwHome(), "config.json")
	cfg := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("%s is damaged: %w", path, err)
		}
	}
	cfg["ollamaUrl"] = engineURL
	if cur, _ := cfg["model"].(string); cur == "" && model != "" {
		cfg["model"] = "ollama:" + model
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return drive.WriteFileAtomic(path, append(raw, '\n'))
}
