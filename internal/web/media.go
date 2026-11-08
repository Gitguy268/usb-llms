package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/Gitguy268/usb-llms/internal/chat"
	"github.com/Gitguy268/usb-llms/internal/media"
	"github.com/Gitguy268/usb-llms/internal/webui"
)

type attachment struct {
	Name string `json:"name"`
	Mime string `json:"mime"`
	Data string `json:"data"` // base64, with or without a data: prefix
}

func decodeB64(s string) ([]byte, error) {
	if i := strings.Index(s, ","); strings.HasPrefix(s, "data:") && i > 0 {
		s = s[i+1:]
	}
	return base64.StdEncoding.DecodeString(s)
}

// attach stores pictures and audio for a message. A model that hears gets the audio itself;
// for other models it is transcribed and the text goes into the message instead.
func (s *Server) attach(ctx context.Context, engineURL, model, text string, files []attachment) (chat.Message, []string, error) {
	msg := chat.Message{Role: "user", Content: text}
	if len(files) == 0 {
		return msg, nil, nil
	}
	if len(files) > 8 {
		return msg, nil, errors.New("attach at most 8 files")
	}
	sees, hears := chat.Senses(ctx, engineURL, model)
	var notes, transcripts []string
	for _, f := range files {
		raw, err := decodeB64(f.Data)
		if err != nil {
			return msg, nil, fmt.Errorf("%s isn't valid base64", f.Name)
		}
		switch {
		case strings.HasPrefix(f.Mime, "image/"):
			if !sees {
				notes = append(notes, model+" can't see pictures, so the picture was left out.")
				continue
			}
			name, err := chat.SaveUpload(s.Drive, raw, media.ExtOf(f.Mime))
			if err != nil {
				return msg, nil, err
			}
			msg.Files = append(msg.Files, name)
		case strings.HasPrefix(f.Mime, "audio/"):
			wav, err := media.ToWav(ctx, raw)
			if err != nil {
				return msg, nil, err
			}
			if hears {
				name, err := chat.SaveUpload(s.Drive, wav, ".wav")
				if err != nil {
					return msg, nil, err
				}
				msg.Files = append(msg.Files, name)
				continue
			}
			out, err := media.Make(ctx, s.Drive, media.Request{Kind: "transcribe", Audio: wav}, nil)
			if err != nil {
				return msg, nil, fmt.Errorf("%s doesn't hear audio and the voice note couldn't be transcribed: %w", model, err)
			}
			transcripts = append(transcripts, "[Voice note, transcribed]: "+*out.Text)
			notes = append(notes, "Transcribed the voice note for "+model+".")
		default:
			notes = append(notes, f.Name+" was left out: only pictures and audio can be attached.")
		}
	}
	if len(transcripts) > 0 {
		msg.Content = strings.TrimSpace(strings.Join(append(transcripts, text), "\n\n"))
	}
	return msg, notes, nil
}

func (s *Server) mediaStatus(w http.ResponseWriter, r *http.Request) {
	_, sd := media.FindSD(s.Drive)
	_, uvErr := exec.LookPath("uv")
	writeJSON(w, map[string]any{
		"catalog": media.Status(r.Context(), s.Drive), "outputs": media.List(s.Drive, 60),
		"sdServer": sd, "online": s.Drive.Online(), "uvOnHost": uvErr == nil || webui.Installed(s.Drive),
	})
}

func (s *Server) mediaMake(w http.ResponseWriter, r *http.Request) {
	var in struct {
		media.Request
		Seed  *float64 `json:"seed"`
		Image string   `json:"image"`
		Audio string   `json:"audio"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	req := in.Request
	if in.Seed != nil {
		v := int64(*in.Seed)
		req.Seed = &v
	}
	var err error
	if in.Image != "" {
		if req.Image, err = decodeB64(in.Image); err != nil {
			fail(w, http.StatusBadRequest, errors.New("the picture isn't valid base64"))
			return
		}
	}
	if in.Audio != "" {
		if req.Audio, err = decodeB64(in.Audio); err != nil {
			fail(w, http.StatusBadRequest, errors.New("the audio isn't valid base64"))
			return
		}
	}
	stream := newLines(w)
	out, err := media.Make(r.Context(), s.Drive, req, func(p media.Progress) { stream.send(p) })
	if err != nil {
		stream.send(map[string]string{"error": err.Error()})
		return
	}
	stream.send(map[string]any{"done": true, "output": out})
}

func (s *Server) mediaPull(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.ID == "" {
		fail(w, http.StatusBadRequest, errors.New("id is required"))
		return
	}
	stream := newLines(w)
	err := media.Pull(r.Context(), s.Drive, in.ID, func(e media.PullEvent) { stream.send(e) }, func(line string) { stream.send(map[string]string{"status": line}) })
	if err != nil {
		stream.send(map[string]string{"error": err.Error()})
		return
	}
	stream.send(map[string]string{"status": "ready", "model": in.ID})
}

func (s *Server) mediaRemoveModel(w http.ResponseWriter, r *http.Request) {
	if err := media.RemoveModel(r.Context(), s.Drive, r.URL.Query().Get("id")); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) mediaRemoveOut(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"removed": media.Remove(s.Drive, r.URL.Query().Get("rel"))})
}

// mediaFile serves results and chat uploads with their own type, sandboxed.
func (s *Server) mediaFile(w http.ResponseWriter, r *http.Request) {
	p, ok := media.File(s.Drive, r.PathValue("rel"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", media.MimeOf(p))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	http.ServeContent(w, r, "", st.ModTime(), f)
}
