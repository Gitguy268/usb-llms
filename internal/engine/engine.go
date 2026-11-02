// Package engine installs and runs the Ollama server that lives on the drive.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Gitguy268/usb-llms/internal/drive"
	"github.com/Gitguy268/usb-llms/internal/netguard"
)

// DefaultPort stays clear of a system Ollama on 11434.
const DefaultPort = 11435

// ContextLength gives agents room for their tools on any machine; Ollama's own default can be 4096.
const ContextLength = 8192

// deadProxy makes the engine's own outbound requests fail while offline.
const deadProxy = "http://127.0.0.1:9"

// State is written to data/run/engine.json while the server runs.
type State struct {
	PID      int    `json:"pid"`
	Port     int    `json:"port"`
	Online   bool   `json:"online"`
	Hostname string `json:"hostname"`
}

// URL is the engine's API base.
func (s State) URL() string { return fmt.Sprintf("http://127.0.0.1:%d", s.Port) }

func statePath(d *drive.Drive) string { return filepath.Join(d.Run(), "engine.json") }

// Env builds the engine's environment. Everything it would write goes to the drive, and
// settings from the host's own Ollama or proxy are dropped so they can't leak in.
func Env(base []string, d *drive.Drive, port int, online bool) []string {
	out := make([]string, 0, len(base)+16)
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "OLLAMA_") || upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "NO_PROXY" || upper == "ALL_PROXY" {
			continue
		}
		out = append(out, kv)
	}
	home := d.EngineHome()
	out = append(out,
		"OLLAMA_HOST=127.0.0.1:"+strconv.Itoa(port),
		"OLLAMA_MODELS="+d.Models(),
		"OLLAMA_NOHISTORY=1",
		"OLLAMA_CONTEXT_LENGTH="+strconv.Itoa(ContextLength),
		"HOME="+home,
		"USERPROFILE="+home,
		"APPDATA="+filepath.Join(home, "AppData"),
		"LOCALAPPDATA="+filepath.Join(home, "AppData"),
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local"),
		"TMPDIR="+d.Tmp(),
		"TEMP="+d.Tmp(),
		"TMP="+d.Tmp(),
	)
	if !online {
		out = append(out, "OLLAMA_NO_CLOUD=1", "HTTP_PROXY="+deadProxy, "HTTPS_PROXY="+deadProxy, "ALL_PROXY="+deadProxy, "NO_PROXY=localhost,127.0.0.1,::1")
	}
	return out
}

// Running returns the state of a live engine started from this drive on this machine.
func Running(d *drive.Drive) (State, bool) {
	var s State
	raw, err := os.ReadFile(statePath(d))
	if err != nil || json.Unmarshal(raw, &s) != nil {
		return s, false
	}
	if host, _ := os.Hostname(); host != s.Hostname || !healthy(s.URL()) {
		return s, false
	}
	return s, true
}

func healthy(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url+"/api/version", nil)
	if err != nil {
		return false
	}
	res, err := netguard.Local().Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == 200
}

func freePort(start int) (int, error) {
	for port := start; port < start+20; port++ {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err == nil {
			l.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("ports %d-%d are all taken", start, start+19)
}

// Ensure returns a running engine in the drive's current mode, starting or restarting it as needed.
func Ensure(d *drive.Drive) (State, error) {
	online := d.Online()
	if s, ok := Running(d); ok {
		if s.Online == online {
			return s, nil
		}
		if err := Stop(d); err != nil {
			return s, err
		}
	}
	m, ok := Installed(d)
	if !ok {
		return State{}, fmt.Errorf("no engine for %s on this drive yet; run `usb-llms setup` while online", drive.Platform())
	}
	d.CleanAppleFiles()
	port, err := freePort(DefaultPort)
	if err != nil {
		return State{}, err
	}
	logPath := filepath.Join(d.Logs(), "engine.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return State{}, err
	}
	defer logFile.Close()
	cmd := exec.Command(filepath.Join(d.Engine(), m.Binary), "serve")
	cmd.Env = Env(os.Environ(), d, port, online)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Dir = d.EngineHome()
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return State{}, explainStart(err)
	}
	host, _ := os.Hostname()
	s := State{PID: cmd.Process.Pid, Port: port, Online: online, Hostname: host}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if healthy(s.URL()) {
			raw, _ := json.Marshal(s)
			return s, drive.WriteFileAtomic(statePath(d), raw)
		}
		select {
		case err := <-exited:
			// Another command may have started the engine a moment earlier.
			if s, ok := Running(d); ok {
				return s, nil
			}
			time.Sleep(time.Second)
			if s, ok := Running(d); ok {
				return s, nil
			}
			return State{}, fmt.Errorf("the engine stopped during startup (%v). Last log lines:\n%s", err, tail(logPath))
		case <-time.After(200 * time.Millisecond):
		}
	}
	_ = cmd.Process.Kill()
	return State{}, fmt.Errorf("the engine didn't answer within 45 seconds. Last log lines:\n%s", tail(logPath))
}

func explainStart(err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%w. If the drive is mounted noexec, remount it: sudo mount -o remount,exec <mount point>", err)
	}
	return err
}

func tail(path string) string {
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

// Stop ends the engine if it is running.
func Stop(d *drive.Drive) error {
	s, ok := Running(d)
	if !ok {
		_ = os.Remove(statePath(d))
		return nil
	}
	p, err := os.FindProcess(s.PID)
	if err != nil {
		return err
	}
	if err := terminate(p); err != nil {
		return err
	}
	for i := 0; i < 50; i++ {
		if !healthy(s.URL()) {
			return os.Remove(statePath(d))
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = p.Kill()
	return os.Remove(statePath(d))
}
