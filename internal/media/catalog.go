// Package media makes images, video, speech, transcripts, 3D models and music with engines
// kept on the drive: stable-diffusion.cpp, the drive's Ollama, and Python sidecars made with uv.
package media

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Gitguy268/usb-llms/internal/drive"
)

// Kinds are what can be made. "listen" is a chat model that hears audio.
var Kinds = []string{"image", "video", "speech", "transcribe", "listen", "music", "3d"}

// Defaults are per-model generation settings.
type Defaults struct {
	Steps     int     `json:"steps,omitempty"`
	CFG       float64 `json:"cfg,omitempty"`
	Width     int     `json:"width,omitempty"`
	Height    int     `json:"height,omitempty"`
	Frames    int     `json:"frames,omitempty"`
	FPS       int     `json:"fps,omitempty"`
	FlowShift float64 `json:"flowShift,omitempty"`
	Sampler   string  `json:"sampler,omitempty"`
	Negative  string  `json:"negative,omitempty"`
}

// Entry is one catalogue model. The same catalog.json ships with LvW-harness.
type Entry struct {
	ID           string              `json:"id"`
	Kinds        []string            `json:"kinds"`
	Engine       string              `json:"engine"`
	Name         string              `json:"name"`
	Licence      string              `json:"licence"`
	SizeGB       float64             `json:"sizeGB"`
	RAMGB        float64             `json:"ramGB"`
	Default      bool                `json:"default,omitempty"`
	Note         string              `json:"note,omitempty"`
	Components   map[string]string   `json:"components,omitempty"`
	Ollama       string              `json:"ollama,omitempty"`
	Args         []string            `json:"args,omitempty"`
	PlatformArgs map[string][]string `json:"platformArgs,omitempty"`
	Defaults     Defaults            `json:"defaults,omitzero"`
}

// Does reports whether the entry makes kind.
func (e Entry) Does(kind string) bool {
	for _, k := range e.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

//go:embed catalog.json
var catalogJSON []byte

type catalogFile struct {
	Entries []Entry `json:"entries"`
}

// Catalog is the built-in list plus data/media-catalog.json on the drive, which wins on id.
func Catalog(d *drive.Drive) []Entry {
	var built catalogFile
	_ = json.Unmarshal(catalogJSON, &built)
	out := built.Entries
	if d != nil {
		if raw, err := os.ReadFile(filepath.Join(d.Data(), "media-catalog.json")); err == nil {
			var own catalogFile
			if json.Unmarshal(raw, &own) == nil {
				for _, e := range own.Entries {
					replaced := false
					for i := range out {
						if out[i].ID == e.ID {
							out[i], replaced = e, true
						}
					}
					if !replaced {
						out = append(out, e)
					}
				}
			}
		}
	}
	return out
}

// Find looks an id up in the catalogue.
func Find(d *drive.Drive, id string) (Entry, bool) {
	for _, e := range Catalog(d) {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// DefaultFor is the configured choice for a kind, else the catalogue default.
func DefaultFor(d *drive.Drive, kind string) (Entry, bool) {
	cfg, _ := d.LoadConfig()
	list := []Entry{}
	for _, e := range Catalog(d) {
		if e.Does(kind) {
			list = append(list, e)
		}
	}
	for _, e := range list {
		if e.ID == cfg.Media.Defaults[kind] {
			return e, true
		}
	}
	for _, e := range list {
		if e.Default {
			return e, true
		}
	}
	if len(list) > 0 {
		return list[0], true
	}
	return Entry{}, false
}

var componentRe = regexp.MustCompile(`^hf:([^/]+/[^/]+)/(.+)$`)

// ParseComponent splits "hf:owner/repo/path/file" into repo and path.
func ParseComponent(spec string) (repo, path string, err error) {
	m := componentRe.FindStringSubmatch(spec)
	if m == nil {
		return "", "", fmt.Errorf("component %q must look like hf:owner/repo/path/to/file", spec)
	}
	return m[1], m[2], nil
}
