// Package drive knows where everything lives. All state stays under one root, so the folder can move between machines.
package drive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Drive is a USB LLMS folder on a stick, an SSD or a local disk.
type Drive struct{ Root string }

// Find resolves the root: --home, then USB_LLMS_HOME, then the folder above bin/ where the binary sits.
func Find(override string) (*Drive, error) {
	root := override
	if root == "" {
		root = os.Getenv("USB_LLMS_HOME")
	}
	if root == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		root = filepath.Dir(exe)
		if filepath.Base(root) == "bin" {
			root = filepath.Dir(root)
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	d := &Drive{Root: abs}
	for _, dir := range []string{d.Models(), d.Chats(), d.Logs(), d.Run(), d.Tmp(), d.EngineHome()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("can't write to %s: %w", abs, err)
		}
	}
	return d, nil
}

// Platform names this machine, e.g. darwin-arm64.
func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

func (d *Drive) Data() string       { return filepath.Join(d.Root, "data") }
func (d *Drive) Models() string     { return filepath.Join(d.Root, "models") }
func (d *Drive) Engine() string     { return filepath.Join(d.Root, "engine", Platform()) }
func (d *Drive) Chats() string      { return filepath.Join(d.Data(), "chats") }
func (d *Drive) Logs() string       { return filepath.Join(d.Data(), "logs") }
func (d *Drive) Run() string        { return filepath.Join(d.Data(), "run") }
func (d *Drive) Tmp() string        { return filepath.Join(d.Data(), "tmp") }
func (d *Drive) EngineHome() string { return filepath.Join(d.Data(), "engine-home") }
func (d *Drive) LvwHome() string    { return filepath.Join(d.Data(), "lvw") }
func (d *Drive) WebUI() string      { return filepath.Join(d.Data(), "webui") }
func (d *Drive) ConfigPath() string { return filepath.Join(d.Data(), "config.json") }

// MediaModels holds image, video, 3D and music weights, which work on every platform.
func (d *Drive) MediaModels() string { return filepath.Join(d.Root, "media") }

// MediaOut holds what the media engines made, plus files attached in chat (uploads/).
func (d *Drive) MediaOut() string { return filepath.Join(d.Data(), "media") }

// Sidecars holds the Python engines for 3D and music: shared weights and per-platform envs.
func (d *Drive) Sidecars() string { return filepath.Join(d.Data(), "sidecar") }

// Remote is an OpenAI-compatible service used in online mode. Only the name of the key's
// environment variable is stored, so a lost drive can't leak the key.
type Remote struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	KeyEnv string `json:"keyEnv,omitempty"`
}

// Config is data/config.json.
type Config struct {
	// Mode is "online", "offline", or empty: offline once a model exists, online until then.
	Mode    string   `json:"mode,omitempty"`
	Remotes []Remote `json:"remotes,omitempty"`
	Media   Media    `json:"media,omitzero"`
}

// Media holds media choices: the catalogue id to use per kind, and an optional remote
// (one of Remotes) with the model names to use there for kinds not set up on the drive.
type Media struct {
	Defaults   map[string]string `json:"defaults,omitempty"`
	API        string            `json:"api,omitempty"`
	Image      string            `json:"image,omitempty"`
	Speech     string            `json:"speech,omitempty"`
	Voice      string            `json:"voice,omitempty"`
	Transcribe string            `json:"transcribe,omitempty"`
}

// LoadConfig returns defaults when no file exists yet.
func (d *Drive) LoadConfig() (Config, error) {
	var c Config
	raw, err := os.ReadFile(d.ConfigPath())
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("%s is damaged: %w", d.ConfigPath(), err)
	}
	return c, nil
}

// SaveConfig writes through a temp file so pulling the stick mid-write can't truncate it.
func (d *Drive) SaveConfig(c Config) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(d.ConfigPath(), append(raw, '\n'))
}

// WriteFileAtomic replaces path in one step.
func WriteFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Online reports the effective mode.
func (d *Drive) Online() bool {
	c, err := d.LoadConfig()
	if err != nil {
		return false
	}
	switch c.Mode {
	case "online":
		return true
	case "offline":
		return false
	}
	return !d.HasModels()
}

// HasModels reports whether the engine's model store holds any manifest.
func (d *Drive) HasModels() bool {
	found := false
	_ = filepath.WalkDir(filepath.Join(d.Models(), "manifests"), func(_ string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// CleanAppleFiles removes the ._* and .DS_Store files macOS leaves on exFAT. In the model
// store they would otherwise be read as manifests by the engine on any system.
func (d *Drive) CleanAppleFiles() {
	_ = filepath.WalkDir(d.Models(), func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && (strings.HasPrefix(e.Name(), "._") || e.Name() == ".DS_Store") {
			_ = os.Remove(p)
		}
		return nil
	})
}
