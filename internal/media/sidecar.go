package media

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Gitguy268/usb-llms/internal/drive"
	"github.com/Gitguy268/usb-llms/internal/engine"
	"github.com/Gitguy268/usb-llms/internal/netguard"
	"github.com/Gitguy268/usb-llms/internal/webui"
)

// The 3D sidecar runs TripoSR (MIT) through media3d.py, the same script LvW-harness uses.
// The music sidecar is ACE-Step 1.5 (MIT code and weights) with its own acestep-api server.
// Weights and sources are shared by every platform; only the Python envs are per platform.
const (
	TripoSRCommit = "107cefdc244c39106fa830359024f6a2f1c78871"
	AceStepTag    = "v0.1.8"
	ThreeDPort    = 11439
	MusicPort     = 11440
	deadProxy     = "http://127.0.0.1:9"
)

//go:embed media3d.py
var media3dPy []byte

func platDir(d *drive.Drive) string { return filepath.Join(d.Sidecars(), drive.Platform()) }

func venvBin(venv, name string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venv, "Scripts", name+".exe")
	}
	return filepath.Join(venv, "bin", name)
}

// SidecarReady reports whether a sidecar is installed for this machine.
func SidecarReady(d *drive.Drive, name string) bool {
	_, err := os.Stat(filepath.Join(platDir(d), name+".ready"))
	return err == nil
}

// SidecarEnv keeps everything the Python engines write on the drive; offline adds the hub's
// offline switches and a dead proxy.
func SidecarEnv(base []string, d *drive.Drive, online bool) []string {
	out := make([]string, 0, len(base)+20)
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(key) {
		case "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "HF_HOME", "VIRTUAL_ENV":
			continue
		}
		out = append(out, kv)
	}
	home := filepath.Join(d.Sidecars(), "home")
	out = append(out,
		"HF_HOME="+filepath.Join(d.Sidecars(), "hf"),
		"U2NET_HOME="+filepath.Join(d.Sidecars(), "u2net"),
		"ACESTEP_PROJECT_ROOT="+filepath.Join(d.Sidecars(), "ace"),
		"UV_PROJECT_ENVIRONMENT="+filepath.Join(platDir(d), "music-venv"),
		"UV_PYTHON_INSTALL_DIR="+filepath.Join(platDir(d), "python"),
		"UV_PYTHON_BIN_DIR="+filepath.Join(platDir(d), "python-bin"),
		"UV_CACHE_DIR="+filepath.Join(d.Sidecars(), "uv-cache"),
		"UV_LINK_MODE=copy", "UV_PYTHON_PREFERENCE=only-managed",
		"PYTORCH_ENABLE_MPS_FALLBACK=1",
		"HOME="+home, "USERPROFILE="+home,
		"TMPDIR="+d.Tmp(), "TEMP="+d.Tmp(), "TMP="+d.Tmp(),
	)
	if !online {
		out = append(out, "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1",
			"HTTP_PROXY="+deadProxy, "HTTPS_PROXY="+deadProxy, "ALL_PROXY="+deadProxy, "NO_PROXY=localhost,127.0.0.1,::1")
	}
	return out
}

func fetchSource(ctx context.Context, d *drive.Drive, client *http.Client, url, dest string) error {
	archive := filepath.Join(d.Tmp(), filepath.Base(dest)+".tar.gz")
	defer os.Remove(archive)
	if err := engine.Download(ctx, client, url, archive, nil); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := engine.Unpack(archive, tmp); err != nil {
		return err
	}
	// GitHub tarballs hold one top folder; keep its contents.
	inner := tmp
	if entries, err := os.ReadDir(tmp); err == nil && len(entries) == 1 && entries[0].IsDir() {
		inner = filepath.Join(tmp, entries[0].Name())
	}
	_ = os.RemoveAll(dest)
	if err := os.Rename(inner, dest); err != nil {
		return err
	}
	return os.RemoveAll(tmp)
}

func retry(times int, fn func() error) error {
	var err error
	for i := 1; i <= times; i++ {
		if err = fn(); err == nil {
			return nil
		}
		time.Sleep(time.Duration(3*i) * time.Second)
	}
	return err
}

// InstallSidecar puts the 3D or music engine on the drive for this machine.
func InstallSidecar(ctx context.Context, d *drive.Drive, client *http.Client, name string, out io.Writer) error {
	if !d.Online() {
		return netguard.ErrOffline
	}
	uv, err := webui.FindUV(ctx, d, client)
	if err != nil {
		return fmt.Errorf("uv: %w", err)
	}
	if err := os.MkdirAll(platDir(d), 0o755); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(platDir(d), name+".ready"))
	env := SidecarEnv(os.Environ(), d, true)
	run := func(dir string, prog string, args ...string) error {
		cmd := exec.CommandContext(ctx, prog, args...)
		cmd.Env, cmd.Dir = env, dir
		cmd.Stdout, cmd.Stderr = out, out
		return cmd.Run()
	}
	say := func(s string) { fmt.Fprintln(out, s) }
	switch name {
	case "3d":
		say("Downloading TripoSR…")
		if err := fetchSource(ctx, d, client, "https://codeload.github.com/VAST-AI-Research/TripoSR/tar.gz/"+TripoSRCommit, filepath.Join(d.Sidecars(), "triposr")); err != nil {
			return err
		}
		venv := filepath.Join(platDir(d), "3d-venv")
		if err := run(d.Sidecars(), uv, "venv", "--allow-existing", "--python", "3.11", venv); err != nil {
			return fmt.Errorf("uv venv: %w", err)
		}
		say("Installing PyTorch and TripoSR's packages (a few GB)…")
		py := venvBin(venv, "python")
		if err := run(d.Sidecars(), uv, "pip", "install", "--python", py, "torch", "omegaconf==2.3.0", "einops", "transformers>=4.40,<4.50", "trimesh", "rembg", "onnxruntime", "huggingface-hub", "PyMCubes", "Pillow", "imageio", "numpy<2.3"); err != nil {
			return fmt.Errorf("uv pip install: %w", err)
		}
		say("Downloading the TripoSR and background-removal weights…")
		script := "from huggingface_hub import hf_hub_download as d; d('stabilityai/TripoSR','config.yaml'); d('stabilityai/TripoSR','model.ckpt'); d('facebook/dino-vitb16','config.json'); import rembg; rembg.new_session()"
		if err := retry(3, func() error { return run(d.Sidecars(), py, "-c", script) }); err != nil {
			return fmt.Errorf("weights: %w", err)
		}
	case "music":
		say("Downloading ACE-Step 1.5…")
		ace := filepath.Join(d.Sidecars(), "ace")
		if err := fetchSource(ctx, d, client, "https://codeload.github.com/ace-step/ACE-Step-1.5/tar.gz/refs/tags/"+AceStepTag, ace); err != nil {
			return err
		}
		say("Installing ACE-Step's packages (several GB)…")
		if err := run(ace, uv, "sync", "--python", "3.12"); err != nil {
			return fmt.Errorf("uv sync: %w", err)
		}
		say("Downloading the ACE-Step weights…")
		if err := retry(3, func() error { return run(ace, uv, "run", "--no-sync", "acestep-download") }); err != nil {
			return fmt.Errorf("weights: %w", err)
		}
	default:
		return fmt.Errorf("no sidecar %q", name)
	}
	_ = os.RemoveAll(filepath.Join(d.Sidecars(), "uv-cache"))
	return os.WriteFile(filepath.Join(platDir(d), name+".ready"), []byte(time.Now().Format(time.RFC3339)), 0o644)
}

// SidecarState is written to data/run/sidecar-<name>.json while it runs.
type SidecarState struct {
	PID      int    `json:"pid"`
	Port     int    `json:"port"`
	Hostname string `json:"hostname"`
}

func sidecarStatePath(d *drive.Drive, name string) string {
	return filepath.Join(d.Run(), "sidecar-"+name+".json")
}

func sidecarHealthy(port int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/health", port), nil)
	res, err := netguard.Local().Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == 200
}

// SidecarRunning returns a live sidecar started from this drive on this machine.
func SidecarRunning(d *drive.Drive, name string) (SidecarState, bool) {
	var s SidecarState
	raw, err := os.ReadFile(sidecarStatePath(d, name))
	if err != nil || json.Unmarshal(raw, &s) != nil {
		return s, false
	}
	host, _ := os.Hostname()
	return s, host == s.Hostname && sidecarHealthy(s.Port)
}

// StopSidecar ends a sidecar.
func StopSidecar(d *drive.Drive, name string) bool {
	s, ok := SidecarRunning(d, name)
	_ = os.Remove(sidecarStatePath(d, name))
	if !ok {
		return false
	}
	if p, err := os.FindProcess(s.PID); err == nil {
		_ = engine.Terminate(p)
	}
	return true
}

// StartSidecar starts (or reuses) a sidecar and waits until its model is loaded. It always runs
// with the offline switches: the weights are on the drive.
func StartSidecar(d *drive.Drive, name string, status func(string)) (SidecarState, error) {
	if s, ok := SidecarRunning(d, name); ok {
		return s, nil
	}
	if !SidecarReady(d, name) {
		return SidecarState{}, fmt.Errorf("the %s engine isn't on this drive for %s; run `usb-llms media install %s` while online", name, drive.Platform(), name)
	}
	start := ThreeDPort
	if name == "music" {
		start = MusicPort
	}
	port, err := engine.FreePort(start)
	if err != nil {
		return SidecarState{}, err
	}
	var cmd *exec.Cmd
	if name == "3d" {
		script := filepath.Join(d.Sidecars(), "media3d.py")
		if err := os.WriteFile(script, media3dPy, 0o644); err != nil {
			return SidecarState{}, err
		}
		cmd = exec.Command(venvBin(filepath.Join(platDir(d), "3d-venv"), "python"), script, "--port", fmt.Sprint(port), "--tsr", filepath.Join(d.Sidecars(), "triposr"))
		cmd.Dir = d.Sidecars()
	} else {
		cmd = exec.Command(venvBin(filepath.Join(platDir(d), "music-venv"), "acestep-api"), "--host", "127.0.0.1", "--port", fmt.Sprint(port))
		cmd.Dir = filepath.Join(d.Sidecars(), "ace")
	}
	_ = os.MkdirAll(filepath.Join(d.Sidecars(), "home"), 0o755)
	cmd.Env = SidecarEnv(os.Environ(), d, false)
	logPath := filepath.Join(d.Logs(), "sidecar-"+name+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return SidecarState{}, err
	}
	defer logFile.Close()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	engine.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return SidecarState{}, err
	}
	host, _ := os.Hostname()
	s := SidecarState{PID: cmd.Process.Pid, Port: port, Hostname: host}
	raw, _ := json.Marshal(s)
	if err := drive.WriteFileAtomic(sidecarStatePath(d, name), raw); err != nil {
		return s, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	if status != nil {
		status(map[string]string{"3d": "Loading TripoSR…", "music": "Loading ACE-Step…"}[name])
	}
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		if sidecarHealthy(port) {
			return s, nil
		}
		select {
		case err := <-exited:
			_ = os.Remove(sidecarStatePath(d, name))
			return SidecarState{}, fmt.Errorf("the %s engine stopped while loading (%v). Last log lines:\n%s", name, err, tailFile(logPath))
		case <-time.After(time.Second):
		}
	}
	StopSidecar(d, name)
	return SidecarState{}, fmt.Errorf("the %s engine didn't start within 10 minutes; see %s", name, logPath)
}

func postJSON(ctx context.Context, url string, body any) ([]byte, int, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := netguard.Local().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	out, err := io.ReadAll(res.Body)
	return out, res.StatusCode, err
}

// Make3D turns a picture into GLB bytes.
func Make3D(ctx context.Context, d *drive.Drive, image []byte, status func(string)) ([]byte, error) {
	s, err := StartSidecar(d, "3d", status)
	if err != nil {
		return nil, err
	}
	if status != nil {
		status("Building the mesh…")
	}
	body, code, err := postJSON(ctx, fmt.Sprintf("http://127.0.0.1:%d/3d", s.Port), map[string]any{"image": base64.StdEncoding.EncodeToString(image), "remove_bg": true, "resolution": 256})
	if err != nil {
		return nil, err
	}
	if code != 200 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return nil, fmt.Errorf("3D failed: %s", strings.TrimPrefix(e.Error, "ValueError: "))
	}
	return body, nil
}

// MakeMusic composes WAV audio through ACE-Step's task API.
func MakeMusic(ctx context.Context, d *drive.Drive, prompt, lyrics string, seconds int, seed *int64, status func(string)) ([]byte, error) {
	s, err := StartSidecar(d, "music", status)
	if err != nil {
		return nil, err
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", s.Port)
	call := func(path string, in, out any) error {
		body, code, err := postJSON(ctx, base+path, in)
		if err != nil {
			return err
		}
		var env struct {
			Data  json.RawMessage `json:"data"`
			Error *string         `json:"error"`
		}
		if json.Unmarshal(body, &env) != nil || code >= 300 || (env.Error != nil && *env.Error != "") {
			return fmt.Errorf("ACE-Step (%d): %.200s", code, body)
		}
		return json.Unmarshal(env.Data, out)
	}
	if strings.TrimSpace(lyrics) == "" {
		lyrics = "[inst]"
	}
	sd := int64(-1)
	if seed != nil {
		sd = *seed
	}
	var task struct {
		TaskID string `json:"task_id"`
	}
	if err := call("/release_task", map[string]any{"prompt": prompt, "lyrics": lyrics, "audio_duration": or(seconds, 30), "audio_format": "wav", "thinking": false, "seed": sd, "batch_size": 1}, &task); err != nil {
		return nil, err
	}
	started := time.Now()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
		var res []struct {
			Status int    `json:"status"`
			Result string `json:"result"`
		}
		if err := call("/query_result", map[string]any{"task_id_list": []string{task.TaskID}}, &res); err != nil {
			return nil, err
		}
		if len(res) == 0 {
			continue
		}
		switch res[0].Status {
		case 2:
			return nil, fmt.Errorf("ACE-Step failed: %s", res[0].Result)
		case 1:
			var files []struct {
				File string `json:"file"`
			}
			if json.Unmarshal([]byte(res[0].Result), &files) != nil || len(files) == 0 {
				return nil, fmt.Errorf("ACE-Step returned no audio")
			}
			req, _ := http.NewRequestWithContext(ctx, "GET", base+files[0].File, nil)
			r, err := netguard.Local().Do(req)
			if err != nil {
				return nil, err
			}
			defer r.Body.Close()
			return io.ReadAll(r.Body)
		}
		if status != nil {
			status(fmt.Sprintf("Composing… %d s", int(time.Since(started).Seconds())))
		}
	}
}
