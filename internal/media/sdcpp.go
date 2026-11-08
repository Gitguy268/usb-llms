package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Gitguy268/usb-llms/internal/drive"
	"github.com/Gitguy268/usb-llms/internal/engine"
	"github.com/Gitguy268/usb-llms/internal/netguard"
)

// SDTag pins leejet/stable-diffusion.cpp (MIT). GitHub publishes a sha256 digest per asset.
const SDTag = "master-1058-7bcd4ce"

// SDPort is where sd-server listens.
const SDPort = 11438

// GitHubAPI is swapped out by tests.
var GitHubAPI = "https://api.github.com"

// SDAssetPattern picks the release asset for a platform. variant selects a GPU build:
// vulkan or rocm on Linux, vulkan or cuda12 on Windows.
func SDAssetPattern(goos, goarch, variant string) (*regexp.Regexp, error) {
	switch {
	case goos == "darwin":
		return regexp.MustCompile(`bin-Darwin-macOS-[\d.]+-arm64\.zip$`), nil // universal: arm64 and x86_64
	case goos == "linux" && goarch == "amd64" && variant == "":
		return regexp.MustCompile(`bin-Linux-Ubuntu-[\d.]+-x86_64\.zip$`), nil
	case goos == "linux" && goarch == "amd64":
		return regexp.MustCompile(`bin-Linux-Ubuntu-[\d.]+-x86_64-` + regexp.QuoteMeta(variant) + `[\w.-]*\.zip$`), nil
	case goos == "windows" && goarch == "amd64":
		if variant == "" {
			variant = "cpu"
		}
		return regexp.MustCompile(`bin-win-` + regexp.QuoteMeta(variant) + `[\w.-]*-x64\.zip$`), nil
	}
	return nil, fmt.Errorf("stable-diffusion.cpp has no prebuilt server for %s-%s; image and video need a remote here (usb-llms media api)", goos, goarch)
}

func sdDir(d *drive.Drive) string { return filepath.Join(d.Engine(), "sd") }

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// FindSD returns the drive's sd-server for this machine.
func FindSD(d *drive.Drive) (string, bool) {
	hit := ""
	_ = filepath.WalkDir(sdDir(d), func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && e.Name() == exe("sd-server") {
			hit = p
			return filepath.SkipAll
		}
		return nil
	})
	return hit, hit != ""
}

// InstallSD downloads sd-server for this machine onto the drive, checking GitHub's digest.
func InstallSD(ctx context.Context, d *drive.Drive, client *http.Client, variant string, progress func(done, total int64)) (string, error) {
	if !d.Online() {
		return "", netguard.ErrOffline
	}
	pattern, err := SDAssetPattern(runtime.GOOS, runtime.GOARCH, variant)
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", GitHubAPI+"/repos/leejet/stable-diffusion.cpp/releases/tags/"+SDTag, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("GitHub returned %d for stable-diffusion.cpp %s", res.StatusCode, SDTag)
	}
	var rel struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
			URL    string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(res.Body).Decode(&rel); err != nil {
		return "", err
	}
	for _, a := range rel.Assets {
		if !pattern.MatchString(a.Name) {
			continue
		}
		want, ok := strings.CutPrefix(a.Digest, "sha256:")
		if !ok {
			return "", fmt.Errorf("GitHub gave no checksum for %s; not installing it", a.Name)
		}
		archive := filepath.Join(d.Tmp(), a.Name)
		defer os.Remove(archive)
		if err := engine.Download(ctx, client, a.URL, archive, progress); err != nil {
			return "", err
		}
		if got, err := engine.HashFile(archive); err != nil {
			return "", err
		} else if got != want {
			return "", fmt.Errorf("%s failed its checksum", a.Name)
		}
		tmp := sdDir(d) + ".tmp"
		_ = os.RemoveAll(tmp)
		if err := engine.Unpack(archive, tmp); err != nil {
			return "", err
		}
		_ = os.RemoveAll(sdDir(d))
		if err := os.Rename(tmp, sdDir(d)); err != nil {
			return "", err
		}
		bin, ok := FindSD(d)
		if !ok {
			return "", fmt.Errorf("%s has no sd-server", a.Name)
		}
		_ = os.Chmod(bin, 0o755)
		return bin, nil
	}
	return "", fmt.Errorf("stable-diffusion.cpp %s has no asset matching %s", SDTag, pattern)
}

// SDState is written to data/run/sd.json while sd-server runs.
type SDState struct {
	PID      int    `json:"pid"`
	Port     int    `json:"port"`
	ModelID  string `json:"modelId"`
	Hostname string `json:"hostname"`
}

func sdStatePath(d *drive.Drive) string { return filepath.Join(d.Run(), "sd.json") }

func sdHealthy(port int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/v1/models", port), nil)
	res, err := netguard.Local().Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == 200
}

// SDRunning returns the live sd-server started from this drive on this machine.
func SDRunning(d *drive.Drive) (SDState, bool) {
	var s SDState
	raw, err := os.ReadFile(sdStatePath(d))
	if err != nil || json.Unmarshal(raw, &s) != nil {
		return s, false
	}
	host, _ := os.Hostname()
	return s, host == s.Hostname && sdHealthy(s.Port)
}

// StopSD ends sd-server.
func StopSD(d *drive.Drive) bool {
	s, ok := SDRunning(d)
	_ = os.Remove(sdStatePath(d))
	if !ok {
		return false
	}
	if p, err := os.FindProcess(s.PID); err == nil {
		_ = engine.Terminate(p)
	}
	return true
}

// SDArgs builds sd-server's command line for a model.
func SDArgs(m Installed, port int, goos string) []string {
	args := []string{"--listen-ip", "127.0.0.1", "--listen-port", fmt.Sprint(port)}
	keys := make([]string, 0, len(m.Files))
	for k := range m.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		flag := "--" + k
		if k == "model" {
			flag = "-m"
		}
		args = append(args, flag, m.Files[k])
	}
	args = append(args, m.Entry.Args...)
	return append(args, m.Entry.PlatformArgs[goos]...)
}

// StartSD runs sd-server with a model, reusing one that already has it loaded.
func StartSD(d *drive.Drive, m Installed, status func(string)) (SDState, error) {
	if s, ok := SDRunning(d); ok && s.ModelID == m.ID {
		return s, nil
	}
	StopSD(d)
	bin, ok := FindSD(d)
	if !ok {
		return SDState{}, fmt.Errorf("sd-server isn't on this drive for %s; run `usb-llms media install image` while online", drive.Platform())
	}
	port, err := engine.FreePort(SDPort)
	if err != nil {
		return SDState{}, err
	}
	logPath := filepath.Join(d.Logs(), "sd-server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return SDState{}, err
	}
	defer logFile.Close()
	cmd := exec.Command(bin, SDArgs(m, port, runtime.GOOS)...)
	cmd.Env = append(os.Environ(), "HOME="+d.EngineHome(), "TMPDIR="+d.Tmp(), "TEMP="+d.Tmp(), "TMP="+d.Tmp())
	cmd.Dir = d.EngineHome()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	engine.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return SDState{}, err
	}
	host, _ := os.Hostname()
	s := SDState{PID: cmd.Process.Pid, Port: port, ModelID: m.ID, Hostname: host}
	raw, _ := json.Marshal(s)
	if err := drive.WriteFileAtomic(sdStatePath(d), raw); err != nil {
		return s, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	if status != nil {
		status("Loading " + m.Entry.Name + "…")
	}
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		if sdHealthy(port) {
			return s, nil
		}
		select {
		case err := <-exited:
			_ = os.Remove(sdStatePath(d))
			return SDState{}, fmt.Errorf("sd-server stopped while loading %s (%v). Last log lines:\n%s", m.Entry.Name, err, tailFile(logPath))
		case <-time.After(500 * time.Millisecond):
		}
	}
	StopSD(d)
	return SDState{}, fmt.Errorf("sd-server didn't load %s within 15 minutes; see %s", m.Entry.Name, logPath)
}

// Request is what to make. Unset numbers use the model's defaults.
type Request struct {
	Kind      string  `json:"kind"`
	Prompt    string  `json:"prompt,omitempty"`
	Image     []byte  `json:"-"`
	Audio     []byte  `json:"-"`
	Model     string  `json:"model,omitempty"`
	Negative  string  `json:"negative,omitempty"`
	Width     int     `json:"width,omitempty"`
	Height    int     `json:"height,omitempty"`
	Steps     int     `json:"steps,omitempty"`
	CFG       float64 `json:"cfg,omitempty"`
	Seed      *int64  `json:"seed,omitempty"`
	Frames    int     `json:"frames,omitempty"`
	Voice     string  `json:"voice,omitempty"`
	Language  string  `json:"language,omitempty"`
	Translate bool    `json:"translate,omitempty"`
	Lyrics    string  `json:"lyrics,omitempty"`
	Seconds   int     `json:"seconds,omitempty"`
}

func or[T comparable](v, fallback T) T {
	var zero T
	if v == zero {
		return fallback
	}
	return v
}

// SDBody is the native async API body for img_gen or vid_gen.
func SDBody(kind string, r Request, e Entry) map[string]any {
	d := e.Defaults
	sample := map[string]any{"sample_steps": or(r.Steps, or(d.Steps, 20)), "guidance": map[string]any{"txt_cfg": or(r.CFG, or(d.CFG, 7.0))}}
	if d.Sampler != "" {
		sample["sample_method"] = d.Sampler
	}
	if d.FlowShift != 0 {
		sample["flow_shift"] = d.FlowShift
	}
	seed := int64(-1)
	if r.Seed != nil {
		seed = *r.Seed
	}
	body := map[string]any{
		"prompt": r.Prompt, "negative_prompt": or(r.Negative, d.Negative),
		"width": or(r.Width, or(d.Width, 512)), "height": or(r.Height, or(d.Height, 512)),
		"seed": seed, "sample_params": sample, "preview": "proj", "preview_interval": 1,
	}
	if len(r.Image) > 0 {
		body["init_image"] = base64.StdEncoding.EncodeToString(r.Image)
		body["strength"] = 0.75
	}
	if kind == "image" {
		body["batch_count"] = 1
		body["output_format"] = "png"
	} else {
		body["video_frames"] = or(r.Frames, or(d.Frames, 17))
		body["fps"] = or(d.FPS, 16)
		body["output_format"] = "webm"
	}
	return body
}

// Progress reports a running job.
type Progress struct {
	Status  string `json:"status"`
	Step    int    `json:"step,omitempty"`
	Total   int    `json:"total,omitempty"`
	Preview string `json:"preview,omitempty"`
}

// SDGenerate submits a job and polls it until it finishes.
func SDGenerate(ctx context.Context, kind string, r Request, s SDState, e Entry, progress func(Progress)) ([]byte, string, error) {
	base := fmt.Sprintf("http://127.0.0.1:%d/sdcpp/v1", s.Port)
	route := "/img_gen"
	if kind == "video" {
		route = "/vid_gen"
	}
	raw, _ := json.Marshal(SDBody(kind, r, e))
	req, _ := http.NewRequestWithContext(ctx, "POST", base+route, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	res, err := netguard.Local().Do(req)
	if err != nil {
		return nil, "", err
	}
	var job struct {
		ID string `json:"id"`
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode >= 300 || json.Unmarshal(body, &job) != nil || job.ID == "" {
		return nil, "", fmt.Errorf("sd-server refused the job (%d): %.300s", res.StatusCode, body)
	}
	defer func() {
		if ctx.Err() != nil {
			req, _ := http.NewRequest("POST", base+"/jobs/"+job.ID+"/cancel", nil)
			if res, err := netguard.Local().Do(req); err == nil {
				res.Body.Close()
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(700 * time.Millisecond):
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", base+"/jobs/"+job.ID, nil)
		res, err := netguard.Local().Do(req)
		if err != nil {
			return nil, "", err
		}
		var j struct {
			Status        string `json:"status"`
			QueuePosition int    `json:"queue_position"`
			Preview       *struct {
				Step  int    `json:"step"`
				Total int    `json:"total_steps"`
				B64   string `json:"b64_json"`
			} `json:"preview"`
			Result *struct {
				Images []struct {
					B64 string `json:"b64_json"`
				} `json:"images"`
				B64  string `json:"b64_json"`
				Mime string `json:"mime_type"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		err = json.NewDecoder(res.Body).Decode(&j)
		res.Body.Close()
		if err != nil {
			return nil, "", fmt.Errorf("sd-server lost job %s", job.ID)
		}
		switch j.Status {
		case "completed":
			if j.Result == nil {
				return nil, "", fmt.Errorf("sd-server finished without a result")
			}
			b64, mime := j.Result.B64, or(j.Result.Mime, "video/webm")
			if kind == "image" && len(j.Result.Images) > 0 {
				b64, mime = j.Result.Images[0].B64, "image/png"
			}
			data, err := base64.StdEncoding.DecodeString(b64)
			return data, mime, err
		case "failed", "cancelled":
			return nil, "", fmt.Errorf("generation %s: %s", j.Status, strings.Trim(string(j.Error), `"{}`))
		}
		if progress != nil {
			p := Progress{Status: "generating"}
			if j.Status == "queued" {
				p.Status = fmt.Sprintf("queued (%d ahead)", j.QueuePosition)
			}
			if j.Preview != nil {
				p.Step, p.Total, p.Preview = j.Preview.Step, j.Preview.Total, j.Preview.B64
			}
			progress(p)
		}
	}
}

func tailFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return strings.Join(lines, "\n")
}
