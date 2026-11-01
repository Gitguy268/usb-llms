// Package web serves the browser interface. The same page backs `usb-llms web` and `usb-llms app`.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Gitguy268/usb-llms/internal/chat"
	"github.com/Gitguy268/usb-llms/internal/drive"
	"github.com/Gitguy268/usb-llms/internal/engine"
	"github.com/Gitguy268/usb-llms/internal/models"
	"github.com/Gitguy268/usb-llms/internal/netguard"
	"github.com/Gitguy268/usb-llms/internal/sys"
	"github.com/Gitguy268/usb-llms/internal/webui"
)

//go:embed ui
var uiFiles embed.FS

// DefaultPort follows the engine's port.
const DefaultPort = 11436

// Server holds the interface's state.
type Server struct {
	Drive *drive.Drive
	// Token is required on every request when set. Only --lan sets it.
	Token string
	mu    sync.Mutex // serialises mode switches
}

// NewToken returns a random access token.
func NewToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Handler returns the routes behind the security checks.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(uiFiles, "ui")
	mux.Handle("GET /", http.FileServerFS(sub))
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/models", s.listModels)
	mux.HandleFunc("DELETE /api/models", s.deleteModel)
	mux.HandleFunc("GET /api/catalog", s.catalog)
	mux.HandleFunc("POST /api/pull", s.pull)
	mux.HandleFunc("POST /api/mode", s.setMode)
	mux.HandleFunc("GET /api/chats", s.listChats)
	mux.HandleFunc("GET /api/chats/{id}", s.getChat)
	mux.HandleFunc("DELETE /api/chats/{id}", s.deleteChat)
	mux.HandleFunc("POST /api/chat", s.chat)
	return s.guard(mux)
}

// guard stops other websites from reaching the interface: a Host check defeats DNS
// rebinding, an Origin check defeats cross-site posts, and the token protects LAN use.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if s.Token == "" && !netguard.IsLoopback(host) {
			http.Error(w, "this address isn't allowed", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					http.Error(w, "cross-site requests aren't allowed", http.StatusForbidden)
					return
				}
			}
		}
		if s.Token != "" && !s.authorised(w, r) {
			http.Error(w, "open the link printed in the terminal, which carries the access token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorised(w http.ResponseWriter, r *http.Request) bool {
	match := func(v string) bool { return subtle.ConstantTimeCompare([]byte(v), []byte(s.Token)) == 1 }
	if c, err := r.Cookie("usb_llms"); err == nil && match(c.Value) {
		return true
	}
	if t := r.URL.Query().Get("token"); t != "" && match(t) {
		http.SetCookie(w, &http.Cookie{Name: "usb_llms", Value: s.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		return true
	}
	return false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	st, running := engine.Running(s.Drive)
	man, installed := engine.Installed(s.Drive)
	free, _ := sys.FreeBytes(s.Drive.Root)
	cfg, _ := s.Drive.LoadConfig()
	writeJSON(w, map[string]any{
		"platform": drive.Platform(), "root": s.Drive.Root, "online": s.Drive.Online(),
		"engineInstalled": installed, "engineVersion": man.Version, "engineVariant": man.Variant,
		"engineRunning": running, "enginePort": st.Port, "freeBytes": free, "ramBytes": sys.RAMBytes(),
		"remotes": len(cfg.Remotes), "webuiInstalled": webui.Installed(s.Drive), "webuiPort": webui.Port,
	})
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	st, err := engine.Ensure(s.Drive)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err)
		return
	}
	list, err := models.List(r.Context(), st.URL())
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	if list == nil {
		list = []models.Installed{}
	}
	remote := []string{}
	if s.Drive.Online() {
		cfg, _ := s.Drive.LoadConfig()
		for _, rm := range cfg.Remotes {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			ids, err := chat.RemoteModels(ctx, s.Drive, rm.Name)
			cancel()
			if err == nil {
				remote = append(remote, ids...)
			}
		}
	}
	writeJSON(w, map[string]any{"models": list, "remote": remote})
}

func (s *Server) deleteModel(w http.ResponseWriter, r *http.Request) {
	st, err := engine.Ensure(s.Drive)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err)
		return
	}
	if err := models.Remove(r.Context(), st.URL(), r.URL.Query().Get("name")); err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) { writeJSON(w, models.Catalog()) }

func (s *Server) pull(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Spec string `json:"spec"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	st, err := engine.Ensure(s.Drive)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err)
		return
	}
	stream := newLines(w)
	model, err := models.New(s.Drive, st.URL()).Pull(r.Context(), in.Spec, in.Name, func(e models.Event) { stream.send(e) })
	if err != nil {
		stream.send(map[string]string{"error": err.Error()})
		return
	}
	stream.send(map[string]string{"status": "ready", "model": model})
}

func (s *Server) setMode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Online bool `json:"online"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := SetMode(s.Drive, in.Online); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]bool{"online": s.Drive.Online()})
}

// SetMode stores the mode and restarts a running engine so the change takes effect at once.
func SetMode(d *drive.Drive, online bool) error {
	cfg, err := d.LoadConfig()
	if err != nil {
		return err
	}
	cfg.Mode = "offline"
	if online {
		cfg.Mode = "online"
	}
	if err := d.SaveConfig(cfg); err != nil {
		return err
	}
	if _, running := engine.Running(d); running {
		_, err = engine.Ensure(d)
	}
	return err
}

func (s *Server) listChats(w http.ResponseWriter, r *http.Request) {
	list, err := chat.List(s.Drive)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, list)
}

func (s *Server) getChat(w http.ResponseWriter, r *http.Request) {
	c, err := chat.Load(s.Drive, r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, errors.New("that chat doesn't exist"))
		return
	}
	writeJSON(w, c)
}

func (s *Server) deleteChat(w http.ResponseWriter, r *http.Request) {
	if err := chat.Delete(s.Drive, r.PathValue("id")); err != nil {
		fail(w, http.StatusNotFound, errors.New("that chat doesn't exist"))
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// chat appends the user's message, streams the reply and saves it, including a reply cut short by Stop.
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if in.Model == "" || strings.TrimSpace(in.Content) == "" {
		fail(w, http.StatusBadRequest, errors.New("choose a model and type a message"))
		return
	}
	conv := chat.Conversation{}
	if in.ID != "" {
		var err error
		if conv, err = chat.Load(s.Drive, in.ID); err != nil {
			fail(w, http.StatusNotFound, errors.New("that chat doesn't exist"))
			return
		}
	}
	st, err := engine.Ensure(s.Drive)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err)
		return
	}
	conv.Model = in.Model
	conv.Messages = append(conv.Messages, chat.Message{Role: "user", Content: in.Content})
	if err := chat.Save(s.Drive, &conv); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	stream := newLines(w)
	stream.send(map[string]string{"id": conv.ID, "title": conv.Title})
	var reply strings.Builder
	err = chat.Stream(r.Context(), s.Drive, st.URL(), in.Model, conv.Messages, func(delta string) {
		reply.WriteString(delta)
		stream.send(map[string]string{"delta": delta})
	})
	if reply.Len() > 0 {
		conv.Messages = append(conv.Messages, chat.Message{Role: "assistant", Content: reply.String()})
		_ = chat.Save(s.Drive, &conv)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		stream.send(map[string]string{"error": err.Error()})
		return
	}
	stream.send(map[string]bool{"done": true})
}

type lines struct {
	w http.ResponseWriter
	f http.Flusher
}

func newLines(w http.ResponseWriter) *lines {
	w.Header().Set("Content-Type", "application/x-ndjson")
	f, _ := w.(http.Flusher)
	return &lines{w, f}
}

func (l *lines) send(v any) {
	_ = json.NewEncoder(l.w).Encode(v)
	if l.f != nil {
		l.f.Flush()
	}
}

// Listen binds the first free port from start. LAN mode listens on every interface.
func Listen(start int, lan bool) (net.Listener, int, error) {
	host := "127.0.0.1"
	if lan {
		host = "0.0.0.0"
	}
	for port := start; port < start+20; port++ {
		l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err == nil {
			return l, port, nil
		}
	}
	return nil, 0, fmt.Errorf("ports %d-%d are all taken", start, start+19)
}

// LANAddress finds this machine's address on the local network.
func LANAddress() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ip, ok := a.(*net.IPNet); ok && ip.IP.IsPrivate() && ip.IP.To4() != nil {
			return ip.IP.String()
		}
	}
	return ""
}
