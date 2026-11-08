package media

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Gitguy268/usb-llms/internal/drive"
)

// Installed is an sd.cpp model on the drive.
type Installed struct {
	ID      string            `json:"id"`
	Entry   Entry             `json:"entry"`
	Files   map[string]string `json:"files"` // component → absolute path (relative on disk)
	Size    int64             `json:"size"`
	AddedAt int64             `json:"addedAt"`
}

var unsafeName = regexp.MustCompile(`[^\w.-]`)

func modelDir(d *drive.Drive, id string) string {
	return filepath.Join(d.MediaModels(), unsafeName.ReplaceAllString(id, "_"))
}

// LoadInstalled returns an installed model whose files are all present.
func LoadInstalled(d *drive.Drive, id string) (Installed, bool) {
	var m Installed
	raw, err := os.ReadFile(filepath.Join(modelDir(d, id), "model.json"))
	if err != nil || json.Unmarshal(raw, &m) != nil {
		return m, false
	}
	// Catalogue fixes (args, defaults) apply to models downloaded before them.
	if e, ok := Find(d, id); ok {
		m.Entry = e
	}
	for k, rel := range m.Files {
		p := filepath.Join(modelDir(d, id), rel)
		if _, err := os.Stat(p); err != nil {
			return m, false
		}
		m.Files[k] = p
	}
	return m, true
}

// ListInstalled lists every complete sd.cpp model on the drive.
func ListInstalled(d *drive.Drive) []Installed {
	entries, _ := os.ReadDir(d.MediaModels())
	out := []Installed{}
	for _, e := range entries {
		var head struct {
			ID string `json:"id"`
		}
		raw, err := os.ReadFile(filepath.Join(d.MediaModels(), e.Name(), "model.json"))
		if err != nil || json.Unmarshal(raw, &head) != nil {
			continue
		}
		if m, ok := LoadInstalled(d, head.ID); ok {
			out = append(out, m)
		}
	}
	return out
}

func saveInstalled(d *drive.Drive, e Entry, files map[string]string, size int64) error {
	rel := map[string]string{}
	for k, p := range files {
		rel[k] = filepath.Base(p)
	}
	raw, _ := json.MarshalIndent(Installed{ID: e.ID, Entry: e, Files: rel, Size: size, AddedAt: time.Now().Unix()}, "", "  ")
	return drive.WriteFileAtomic(filepath.Join(modelDir(d, e.ID), "model.json"), raw)
}

// MIME maps the extensions media files use.
var MIME = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif",
	".wav": "audio/wav", ".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".flac": "audio/flac", ".m4a": "audio/mp4",
	".mp4": "video/mp4", ".webm": "video/webm", ".avi": "video/x-msvideo",
	".glb": "model/gltf-binary", ".txt": "text/plain; charset=utf-8", ".json": "application/json",
}

// MimeOf guesses a file's type from its extension.
func MimeOf(p string) string {
	if m, ok := MIME[strings.ToLower(filepath.Ext(p))]; ok {
		return m
	}
	return "application/octet-stream"
}

// ExtOf is the extension for a MIME type.
func ExtOf(mime string) string {
	mime, _, _ = strings.Cut(mime, ";")
	for ext, m := range MIME {
		if strings.HasPrefix(m, mime) && ext != ".jpeg" {
			return ext
		}
	}
	return ".bin"
}

// Output is one thing made, saved as data/media/<date>/<kind>-<time>.<ext> with a .json beside it.
type Output struct {
	Kind string         `json:"kind"`
	Path string         `json:"path"`
	Rel  string         `json:"rel"`
	Mime string         `json:"mime"`
	Text *string        `json:"text,omitempty"`
	Meta map[string]any `json:"meta"`
}

// Save writes a result and its settings.
func Save(d *drive.Drive, kind string, data []byte, ext string, meta map[string]any, text *string) (Output, error) {
	day := time.Now().Format("2006-01-02")
	dir := filepath.Join(d.MediaOut(), day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Output{}, err
	}
	stamp := time.Now().Format("150405")
	name := kind + "-" + stamp + ext
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("%s-%s-%d%s", kind, stamp, i, ext)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return Output{}, err
	}
	out := Output{Kind: kind, Path: p, Rel: day + "/" + name, Mime: MimeOf(p), Text: text, Meta: meta}
	side := map[string]any{"kind": kind, "mime": out.Mime, "createdAt": time.Now().Unix()}
	for k, v := range meta {
		side[k] = v
	}
	if text != nil {
		side["text"] = *text
	}
	raw, _ := json.MarshalIndent(side, "", "  ")
	return out, os.WriteFile(p+".json", raw, 0o644)
}

// List returns the newest results first.
func List(d *drive.Drive, limit int) []Output {
	days, _ := os.ReadDir(d.MediaOut())
	sort.Slice(days, func(i, j int) bool { return days[i].Name() > days[j].Name() })
	out := []Output{}
	for _, day := range days {
		if !day.IsDir() || day.Name() == "uploads" {
			continue
		}
		dir := filepath.Join(d.MediaOut(), day.Name())
		files, _ := os.ReadDir(dir)
		type item struct {
			name string
			mod  time.Time
		}
		items := []item{}
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, strings.TrimSuffix(f.Name(), ".json"))); err != nil {
				continue
			}
			info, _ := f.Info()
			items = append(items, item{f.Name(), info.ModTime()})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
		for _, it := range items {
			var meta map[string]any
			raw, _ := os.ReadFile(filepath.Join(dir, it.name))
			if json.Unmarshal(raw, &meta) != nil {
				continue
			}
			p := filepath.Join(dir, strings.TrimSuffix(it.name, ".json"))
			o := Output{Kind: fmt.Sprint(meta["kind"]), Path: p, Rel: day.Name() + "/" + filepath.Base(p), Mime: MimeOf(p), Meta: meta}
			if t, ok := meta["text"].(string); ok {
				o.Text = &t
			}
			out = append(out, o)
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

// File resolves a /media/<rel> path inside the media folder, refusing anything outside it.
func File(d *drive.Drive, rel string) (string, bool) {
	if rel == "" || strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return "", false
	}
	p := filepath.Join(d.MediaOut(), filepath.FromSlash(rel))
	st, err := os.Stat(p)
	return p, err == nil && st.Mode().IsRegular()
}

// Remove deletes a result and its settings file.
func Remove(d *drive.Drive, rel string) bool {
	p, ok := File(d, rel)
	if !ok {
		return false
	}
	_ = os.Remove(p + ".json")
	return os.Remove(p) == nil
}
