package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os/exec"
	"strings"
)

// Target is an OpenAI-style API: the drive's Ollama (/v1) or a configured remote.
type Target struct {
	BaseURL string
	Key     string
	Client  *http.Client
}

func (t Target) do(req *http.Request, what string) ([]byte, error) {
	if t.Key != "" {
		req.Header.Set("Authorization", "Bearer "+t.Key)
	}
	res, err := t.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		msg := string(body)
		var j struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(body, &j) == nil && len(j.Error) > 0 {
			var obj struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(j.Error, &obj) == nil && obj.Message != "" {
				msg = obj.Message
			} else {
				msg = strings.Trim(string(j.Error), `"`)
			}
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("%s failed (%d): %s", what, res.StatusCode, msg)
	}
	return body, nil
}

// Speech reads text aloud and returns WAV bytes.
func Speech(ctx context.Context, t Target, model, input, voice string) ([]byte, error) {
	body := map[string]any{"model": model, "input": input, "response_format": "wav"}
	if voice != "" {
		body["voice"] = voice
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", t.BaseURL+"/audio/speech", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return t.do(req, "Text to speech")
}

// Transcribe turns speech into text; translate gives English.
func Transcribe(ctx context.Context, t Target, model string, audio []byte, language string, translate bool) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", "audio.wav")
	_, _ = fw.Write(audio)
	_ = w.WriteField("model", model)
	_ = w.WriteField("response_format", "json")
	if language != "" {
		_ = w.WriteField("language", language)
	}
	_ = w.Close()
	route := "/audio/transcriptions"
	if translate {
		route = "/audio/translations"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", t.BaseURL+route, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	body, err := t.do(req, "Transcription")
	if err != nil {
		return "", err
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.Text), nil
}

// Image asks an OpenAI-style API for a picture.
func Image(ctx context.Context, t Target, model, prompt string, width, height int) ([]byte, error) {
	size := "1024x1024"
	if width > 0 && height > 0 {
		size = fmt.Sprintf("%dx%d", width, height)
	}
	raw, _ := json.Marshal(map[string]any{"model": model, "prompt": prompt, "size": size, "n": 1})
	req, err := http.NewRequestWithContext(ctx, "POST", t.BaseURL+"/images/generations", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	body, err := t.do(req, "Image generation")
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			B64 string `json:"b64_json"`
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Data) == 0 {
		return nil, fmt.Errorf("the image API returned no image")
	}
	if out.Data[0].B64 != "" {
		return base64.StdEncoding.DecodeString(out.Data[0].B64)
	}
	req, err = http.NewRequestWithContext(ctx, "GET", out.Data[0].URL, nil)
	if err != nil {
		return nil, err
	}
	return t.do(req, "Downloading the image")
}

// IsWav reports a RIFF/WAVE file.
func IsWav(b []byte) bool {
	return len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE"
}

// ToWav converts other audio with the host's ffmpeg when there is one; the engines read WAV.
func ToWav(ctx context.Context, b []byte) ([]byte, error) {
	if IsWav(b) {
		return b, nil
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("only WAV audio can be read without ffmpeg; convert the file to WAV first")
	}
	cmd := exec.CommandContext(ctx, ffmpeg, "-loglevel", "error", "-i", "pipe:0", "-ar", "16000", "-ac", "1", "-f", "wav", "pipe:1")
	cmd.Stdin = bytes.NewReader(b)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg couldn't read the audio: %.200s", strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}
