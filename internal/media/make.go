package media

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Gitguy268/usb-llms/internal/chat"
	"github.com/Gitguy268/usb-llms/internal/drive"
	"github.com/Gitguy268/usb-llms/internal/engine"
	"github.com/Gitguy268/usb-llms/internal/models"
	"github.com/Gitguy268/usb-llms/internal/netguard"
	"github.com/Gitguy268/usb-llms/internal/sys"
)

// HFEndpoint is swapped out by tests.
var HFEndpoint = "https://huggingface.co"

// engineModels lists what the drive's Ollama holds, without the :latest tag.
func engineModels(ctx context.Context, d *drive.Drive) map[string]bool {
	out := map[string]bool{}
	st, ok := engine.Running(d)
	if !ok {
		return out
	}
	list, err := models.List(ctx, st.URL())
	if err != nil {
		return out
	}
	for _, m := range list {
		out[strings.TrimSuffix(m.Name, ":latest")] = true
	}
	return out
}

// Ready reports whether an entry can run now.
func Ready(ctx context.Context, d *drive.Drive, e Entry, have map[string]bool) bool {
	switch e.Engine {
	case "sdcpp":
		_, sd := FindSD(d)
		_, ok := LoadInstalled(d, e.ID)
		return sd && ok
	case "ollama":
		return have[e.Ollama]
	case "triposr":
		return SidecarReady(d, "3d")
	case "acestep":
		return SidecarReady(d, "music")
	}
	return false
}

// Row is a catalogue entry with whether it is ready here.
type Row struct {
	Entry
	Ready bool `json:"ready"`
}

// Status is the catalogue with readiness.
func Status(ctx context.Context, d *drive.Drive) []Row {
	have := engineModels(ctx, d)
	out := []Row{}
	for _, e := range Catalog(d) {
		out = append(out, Row{e, Ready(ctx, d, e, have)})
	}
	return out
}

func pick(ctx context.Context, d *drive.Drive, kind, model string) (Entry, bool, error) {
	if model == "api" {
		return Entry{}, false, nil
	}
	if model != "" {
		e, ok := Find(d, model)
		if !ok {
			return e, false, fmt.Errorf("no media model %q; see `usb-llms media ls`", model)
		}
		if !e.Does(kind) {
			return e, false, fmt.Errorf("%s can't make %s", e.Name, kind)
		}
		return e, true, nil
	}
	have := engineModels(ctx, d)
	if e, ok := DefaultFor(d, kind); ok && Ready(ctx, d, e, have) {
		return e, true, nil
	}
	for _, e := range Catalog(d) {
		if e.Does(kind) && Ready(ctx, d, e, have) {
			return e, true, nil
		}
	}
	return Entry{}, false, nil
}

// remote returns the configured media API, if there is one and the drive is online.
func remote(d *drive.Drive) (Target, drive.Media, bool) {
	cfg, _ := d.LoadConfig()
	if cfg.Media.API == "" || !d.Online() {
		return Target{}, cfg.Media, false
	}
	r, key, err := chat.FindRemote(d, cfg.Media.API)
	if err != nil {
		return Target{}, cfg.Media, false
	}
	return Target{BaseURL: strings.TrimSuffix(r.URL, "/"), Key: key, Client: netguard.Client(true)}, cfg.Media, true
}

func noEngine(kind string) error {
	return fmt.Errorf("nothing is set up to make %s; run `usb-llms media install %s` while online, or set a remote with `usb-llms media api`", kind, kind)
}

// Make makes one piece of media and saves it under data/media.
func Make(ctx context.Context, d *drive.Drive, r Request, progress func(Progress)) (Output, error) {
	say := func(s string) {
		if progress != nil {
			progress(Progress{Status: s})
		}
	}
	switch r.Kind {
	case "image", "video", "speech", "music":
		if strings.TrimSpace(r.Prompt) == "" {
			return Output{}, fmt.Errorf("making %s needs a prompt", r.Kind)
		}
	case "transcribe":
		if len(r.Audio) == 0 {
			return Output{}, fmt.Errorf("transcription needs audio")
		}
	case "3d":
		if len(r.Image) == 0 && strings.TrimSpace(r.Prompt) == "" {
			return Output{}, fmt.Errorf("3D needs a picture or a prompt")
		}
		if len(r.Image) == 0 {
			say("Drawing the object first…")
			pic, err := Make(ctx, d, Request{Kind: "image", Prompt: r.Prompt + ", a single object, centered, whole object in view, plain light grey background, studio lighting", Seed: r.Seed}, progress)
			if err != nil {
				return Output{}, err
			}
			img, err := os.ReadFile(pic.Path)
			if err != nil {
				return Output{}, err
			}
			r.Image = img
			out, err := Make(ctx, d, r, progress)
			if err == nil {
				out.Meta["sourceImage"] = pic.Rel
			}
			return out, err
		}
	default:
		return Output{}, fmt.Errorf("unknown kind %q", r.Kind)
	}
	e, local, err := pick(ctx, d, r.Kind, r.Model)
	if err != nil {
		return Output{}, err
	}
	meta := map[string]any{"prompt": r.Prompt, "model": "api"}
	if r.Seed != nil {
		meta["seed"] = *r.Seed
	}
	if local {
		meta["model"] = e.ID
	}
	api, mc, hasAPI := remote(d)

	switch r.Kind {
	case "image", "video":
		if local {
			m, ok := LoadInstalled(d, e.ID)
			if !ok {
				return Output{}, fmt.Errorf("%s isn't on the drive; run `usb-llms media pull %s`", e.Name, e.ID)
			}
			st, err := StartSD(d, m, say)
			if err != nil {
				return Output{}, err
			}
			data, mime, err := SDGenerate(ctx, r.Kind, r, st, e, progress)
			if err != nil {
				return Output{}, err
			}
			meta["width"], meta["height"] = or(r.Width, e.Defaults.Width), or(r.Height, e.Defaults.Height)
			return Save(d, r.Kind, data, ExtOf(mime), meta, nil)
		}
		if r.Kind == "image" && hasAPI && mc.Image != "" {
			say("Asking the image API…")
			data, err := Image(ctx, api, mc.Image, r.Prompt, r.Width, r.Height)
			if err != nil {
				return Output{}, err
			}
			meta["model"] = "api:" + mc.Image
			return Save(d, "image", data, ".png", meta, nil)
		}
		return Output{}, noEngine(r.Kind)
	case "speech", "transcribe":
		var t Target
		var model string
		if local {
			st, err := engine.Ensure(d)
			if err != nil {
				return Output{}, err
			}
			t, model = Target{BaseURL: st.URL() + "/v1", Client: netguard.Local()}, e.Ollama
		} else if hasAPI && ((r.Kind == "speech" && mc.Speech != "") || (r.Kind == "transcribe" && mc.Transcribe != "")) {
			t, model = api, mc.Speech
			if r.Kind == "transcribe" {
				model = mc.Transcribe
			}
			meta["model"] = "api:" + model
		} else {
			return Output{}, noEngine(r.Kind)
		}
		if r.Kind == "speech" {
			say("Speaking…")
			voice := r.Voice
			if voice == "" && !local {
				voice = mc.Voice
			}
			wav, err := Speech(ctx, t, model, r.Prompt, voice)
			if err != nil {
				return Output{}, err
			}
			meta["voice"] = voice
			return Save(d, "speech", wav, ".wav", meta, nil)
		}
		say("Listening…")
		audio := r.Audio
		if local {
			if audio, err = ToWav(ctx, audio); err != nil {
				return Output{}, err
			}
		}
		text, err := Transcribe(ctx, t, model, audio, r.Language, r.Translate)
		if err != nil {
			return Output{}, err
		}
		return Save(d, "transcribe", []byte(text), ".txt", meta, &text)
	case "3d":
		if !local {
			return Output{}, noEngine("3d")
		}
		glb, err := Make3D(ctx, d, r.Image, say)
		if err != nil {
			return Output{}, err
		}
		return Save(d, "3d", glb, ".glb", meta, nil)
	case "music":
		if !local {
			return Output{}, noEngine("music")
		}
		wav, err := MakeMusic(ctx, d, r.Prompt, r.Lyrics, r.Seconds, r.Seed, say)
		if err != nil {
			return Output{}, err
		}
		meta["seconds"], meta["lyrics"] = or(r.Seconds, 30), r.Lyrics
		return Save(d, "music", wav, ".wav", meta, nil)
	}
	return Output{}, fmt.Errorf("unknown kind %q", r.Kind)
}

// PullEvent reports download progress.
type PullEvent = models.Event

// Pull puts a catalogue entry on the drive: component files for sd.cpp, an engine pull for
// audio models, or the sidecar for 3D and music.
func Pull(ctx context.Context, d *drive.Drive, id string, progress func(PullEvent), log func(string)) error {
	e, ok := Find(d, id)
	if !ok {
		return fmt.Errorf("no media model %q; see `usb-llms media ls`", id)
	}
	if !d.Online() {
		return netguard.ErrOffline
	}
	client := netguard.Client(true)
	switch e.Engine {
	case "ollama":
		st, err := engine.Ensure(d)
		if err != nil {
			return err
		}
		_, err = models.New(d, st.URL()).Pull(ctx, "ollama:"+e.Ollama, "", progress)
		return err
	case "triposr", "acestep":
		name := map[string]string{"triposr": "3d", "acestep": "music"}[e.Engine]
		return InstallSidecar(ctx, d, client, name, lineWriter(log))
	}
	if _, ok := FindSD(d); !ok {
		if log != nil {
			log("Installing stable-diffusion.cpp…")
		}
		if _, err := InstallSD(ctx, d, client, "", progress2(progress, "sd-server")); err != nil {
			return err
		}
	}
	plan := []hfEntry{}
	var need int64
	for flag, spec := range e.Components {
		repo, path, err := ParseComponent(spec)
		if err != nil {
			return err
		}
		f, err := hfFile(ctx, client, repo, path)
		if err != nil {
			return err
		}
		f.flag = flag
		plan = append(plan, f)
		need += f.size
	}
	if free, err := sys.FreeBytes(d.Root); err == nil && uint64(need) > free {
		return fmt.Errorf("%s needs %s and the drive has %s free", e.Name, sys.GB(uint64(need)), sys.GB(free))
	}
	dir := modelDir(d, e.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	files := map[string]string{}
	for _, f := range plan {
		dest := filepath.Join(dir, f.name)
		if st, err := os.Stat(dest); err != nil || st.Size() != f.size {
			part := dest + ".part"
			if err := engine.Download(ctx, client, f.url, part, progress2(progress, f.name)); err != nil {
				return err
			}
			if f.sha != "" {
				if got, err := engine.HashFile(part); err != nil {
					return err
				} else if got != f.sha {
					os.Remove(part)
					return fmt.Errorf("%s failed its checksum", f.name)
				}
			}
			if err := os.Rename(part, dest); err != nil {
				return err
			}
		}
		files[f.flag] = dest
	}
	return saveInstalled(d, e, files, need)
}

func progress2(p func(PullEvent), name string) func(done, total int64) {
	return func(done, total int64) {
		if p != nil {
			p(PullEvent{Status: "downloading " + name, Done: done, Total: total})
		}
	}
}

type lineWriter func(string)

func (w lineWriter) Write(b []byte) (int, error) {
	if w != nil {
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				w(line)
			}
		}
	}
	return len(b), nil
}

// hfFile looks one file up in a Hugging Face repo, with its size and LFS checksum.
type hfEntry struct {
	flag, name, url, sha string
	size                 int64
}

func hfFile(ctx context.Context, client *http.Client, repo, path string) (hfEntry, error) {
	var out hfEntry
	dir := ""
	if i := strings.LastIndex(path, "/"); i >= 0 {
		dir = "/" + path[:i]
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", HFEndpoint+"/api/models/"+repo+"/tree/main"+dir, nil)
	if t := os.Getenv("HF_TOKEN"); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	res, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return out, fmt.Errorf("Hugging Face returned %d for %s", res.StatusCode, repo)
	}
	var entries []struct {
		Type string `json:"type"`
		Path string `json:"path"`
		Size int64  `json:"size"`
		LFS  *struct {
			OID  string `json:"oid"`
			Size int64  `json:"size"`
		} `json:"lfs"`
	}
	if err := json.NewDecoder(res.Body).Decode(&entries); err != nil {
		return out, err
	}
	for _, en := range entries {
		if en.Type != "file" || en.Path != path {
			continue
		}
		out.name, out.size = filepath.Base(path), en.Size
		if en.LFS != nil {
			out.sha, out.size = en.LFS.OID, en.LFS.Size
		}
		parts := strings.Split(path, "/")
		for i := range parts {
			parts[i] = url.PathEscape(parts[i])
		}
		out.url = HFEndpoint + "/" + repo + "/resolve/main/" + strings.Join(parts, "/")
		return out, nil
	}
	return out, fmt.Errorf("%s has no file %s", repo, path)
}

// RemoveModel deletes a media model from the drive.
func RemoveModel(ctx context.Context, d *drive.Drive, id string) error {
	e, ok := Find(d, id)
	if !ok {
		return fmt.Errorf("no media model %q", id)
	}
	switch e.Engine {
	case "ollama":
		st, err := engine.Ensure(d)
		if err != nil {
			return err
		}
		return models.Remove(ctx, st.URL(), e.Ollama)
	case "triposr", "acestep":
		name := map[string]string{"triposr": "3d", "acestep": "music"}[e.Engine]
		StopSidecar(d, name)
		return os.Remove(filepath.Join(platDir(d), name+".ready"))
	}
	if s, ok := SDRunning(d); ok && s.ModelID == id {
		StopSD(d)
	}
	return os.RemoveAll(modelDir(d, id))
}
