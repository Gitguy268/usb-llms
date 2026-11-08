// Package chat streams replies from the drive's engine or a remote service, and stores conversations on the drive.
package chat

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Gitguy268/usb-llms/internal/drive"
	"github.com/Gitguy268/usb-llms/internal/netguard"
)

// Message is one turn. Files are pictures and audio attached to it, as paths under
// data/media/uploads, so a saved chat stays small; they are read when the message is sent.
type Message struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Files   []string `json:"files,omitempty"`
}

// UploadsDir holds files attached in chat.
func UploadsDir(d *drive.Drive) string { return filepath.Join(d.MediaOut(), "uploads") }

// SaveUpload stores an attached file and returns its name for Message.Files.
func SaveUpload(d *drive.Drive, data []byte, ext string) (string, error) {
	if err := os.MkdirAll(UploadsDir(d), 0o755); err != nil {
		return "", err
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	name := hex.EncodeToString(b) + ext
	return name, os.WriteFile(filepath.Join(UploadsDir(d), name), data, 0o644)
}

func readUpload(d *drive.Drive, name string) ([]byte, string, bool) {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return nil, "", false
	}
	raw, err := os.ReadFile(filepath.Join(UploadsDir(d), name))
	if err != nil {
		return nil, "", false
	}
	return raw, strings.ToLower(filepath.Ext(name)), true
}

// engineMessages adds the files as base64 images; Ollama reads pictures and WAV audio from there.
func engineMessages(d *drive.Drive, messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		wire := map[string]any{"role": m.Role, "content": m.Content}
		var images []string
		for _, f := range m.Files {
			if raw, _, ok := readUpload(d, f); ok {
				images = append(images, base64.StdEncoding.EncodeToString(raw))
			}
		}
		if len(images) > 0 {
			wire["images"] = images
		}
		out = append(out, wire)
	}
	return out
}

// remoteMessages uses OpenAI content parts: image_url for pictures, input_audio for WAV.
func remoteMessages(d *drive.Drive, messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		if len(m.Files) == 0 {
			out = append(out, map[string]any{"role": m.Role, "content": m.Content})
			continue
		}
		parts := []map[string]any{{"type": "text", "text": m.Content}}
		for _, f := range m.Files {
			raw, ext, ok := readUpload(d, f)
			if !ok {
				continue
			}
			b64 := base64.StdEncoding.EncodeToString(raw)
			if ext == ".wav" {
				parts = append(parts, map[string]any{"type": "input_audio", "input_audio": map[string]string{"data": b64, "format": "wav"}})
			} else {
				mime := map[string]string{".png": "image/png", ".webp": "image/webp", ".gif": "image/gif"}[ext]
				if mime == "" {
					mime = "image/jpeg"
				}
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + mime + ";base64," + b64}})
			}
		}
		out = append(out, map[string]any{"role": m.Role, "content": parts})
	}
	return out
}

// Senses asks the engine whether a model sees pictures and hears audio.
func Senses(ctx context.Context, engineURL, model string) (sees, hears bool) {
	if strings.HasPrefix(model, RemotePrefix) {
		return true, false
	}
	raw, _ := json.Marshal(map[string]string{"model": model})
	req, err := http.NewRequestWithContext(ctx, "POST", engineURL+"/api/show", bytes.NewReader(raw))
	if err != nil {
		return false, false
	}
	res, err := netguard.Local().Do(req)
	if err != nil {
		return false, false
	}
	defer res.Body.Close()
	var out struct {
		Capabilities []string `json:"capabilities"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	for _, c := range out.Capabilities {
		sees = sees || c == "vision"
		hears = hears || c == "audio"
	}
	return sees, hears
}

// RemotePrefix marks a model served by a configured remote, as remote/<service>/<model>.
const RemotePrefix = "remote/"

// Stream sends messages to model and calls onDelta for each piece of the reply.
func Stream(ctx context.Context, d *drive.Drive, engineURL, model string, messages []Message, onDelta func(string)) error {
	if strings.HasPrefix(model, RemotePrefix) {
		return streamRemote(ctx, d, model, messages, onDelta)
	}
	raw, _ := json.Marshal(map[string]any{"model": model, "messages": engineMessages(d, messages), "stream": true})
	req, err := http.NewRequestWithContext(ctx, "POST", engineURL+"/api/chat", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	res, err := netguard.Local().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var ev struct {
			Error   string `json:"error"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Error != "" {
			return fmt.Errorf("engine: %s", ev.Error)
		}
		if ev.Message.Content != "" {
			onDelta(ev.Message.Content)
		}
	}
	if res.StatusCode >= 400 {
		return fmt.Errorf("engine returned HTTP %d", res.StatusCode)
	}
	return sc.Err()
}

// FindRemote returns a configured remote and its key from the environment.
func FindRemote(d *drive.Drive, name string) (drive.Remote, string, error) {
	cfg, err := d.LoadConfig()
	if err != nil {
		return drive.Remote{}, "", err
	}
	for _, r := range cfg.Remotes {
		if r.Name != name {
			continue
		}
		key := ""
		if r.KeyEnv != "" {
			if key = os.Getenv(r.KeyEnv); key == "" {
				return r, "", fmt.Errorf("set the %s environment variable to use %s", r.KeyEnv, name)
			}
		}
		if err := CheckRemoteURL(r.URL, key != ""); err != nil {
			return r, "", err
		}
		return r, key, nil
	}
	return drive.Remote{}, "", fmt.Errorf("no remote named %q", name)
}

// CheckRemoteURL refuses to send a key in the clear to another machine.
func CheckRemoteURL(raw string, hasKey bool) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("%q isn't an http(s) URL", raw)
	}
	if hasKey && u.Scheme == "http" && !netguard.IsLoopback(u.Hostname()) {
		return fmt.Errorf("%s would send your API key unencrypted; use https", raw)
	}
	return nil
}

func streamRemote(ctx context.Context, d *drive.Drive, model string, messages []Message, onDelta func(string)) error {
	rest := strings.TrimPrefix(model, RemotePrefix)
	name, id, ok := strings.Cut(rest, "/")
	if !ok || id == "" {
		return fmt.Errorf("bad remote model %q; expected remote/<service>/<model>", model)
	}
	r, key, err := FindRemote(d, name)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"model": id, "messages": remoteMessages(d, messages), "stream": true})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(r.URL, "/")+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := netguard.Client(d.Online()).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
		return fmt.Errorf("%s returned HTTP %d: %s", name, res.StatusCode, strings.TrimSpace(string(msg)))
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &ev) == nil && len(ev.Choices) > 0 && ev.Choices[0].Delta.Content != "" {
			onDelta(ev.Choices[0].Delta.Content)
		}
	}
	return sc.Err()
}

// RemoteModels asks a remote which models it offers, as remote/<service>/<model> ids.
func RemoteModels(ctx context.Context, d *drive.Drive, name string) ([]string, error) {
	r, key, err := FindRemote(d, name)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(r.URL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := netguard.Client(d.Online()).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("%s returned HTTP %d", name, res.StatusCode)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, RemotePrefix+name+"/"+m.ID)
	}
	return ids, nil
}

// Conversation is one saved chat, in data/chats/<id>.json.
type Conversation struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Updated  int64     `json:"updated"`
}

func chatPath(d *drive.Drive, id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return "", fmt.Errorf("bad chat id")
	}
	return filepath.Join(d.Chats(), id+".json"), nil
}

// Save writes a conversation, giving it an id and title if it has none.
func Save(d *drive.Drive, c *Conversation) error {
	if c.ID == "" {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		c.ID = hex.EncodeToString(b)
	}
	if c.Title == "" {
		for _, m := range c.Messages {
			if m.Role == "user" {
				c.Title = strings.Join(strings.Fields(m.Content), " ")
				if r := []rune(c.Title); len(r) > 48 {
					c.Title = string(r[:48]) + "…"
				}
				break
			}
		}
	}
	c.Updated = time.Now().Unix()
	p, err := chatPath(d, c.ID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return drive.WriteFileAtomic(p, raw)
}

// Load reads one conversation.
func Load(d *drive.Drive, id string) (Conversation, error) {
	var c Conversation
	p, err := chatPath(d, id)
	if err != nil {
		return c, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(raw, &c)
}

// Delete removes one conversation.
func Delete(d *drive.Drive, id string) error {
	p, err := chatPath(d, id)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// List returns conversations without their messages, newest first.
func List(d *drive.Drive) ([]Conversation, error) {
	entries, err := os.ReadDir(d.Chats())
	if err != nil {
		return nil, err
	}
	out := []Conversation{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), "._") {
			continue
		}
		c, err := Load(d, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		c.Messages = nil
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated > out[j].Updated })
	return out, nil
}
