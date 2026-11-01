package models

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Gitguy268/usb-llms/internal/drive"
	"github.com/Gitguy268/usb-llms/internal/engine"
	"github.com/Gitguy268/usb-llms/internal/netguard"
	"github.com/Gitguy268/usb-llms/internal/sys"
)

// Event reports pull progress.
type Event struct {
	Status string `json:"status"`
	Done   int64  `json:"done,omitempty"`
	Total  int64  `json:"total,omitempty"`
	Model  string `json:"model,omitempty"`
}

// Puller adds models to the engine. Client carries the offline guard for outside traffic.
type Puller struct {
	Drive     *drive.Drive
	Client    *http.Client
	EngineURL string
	GitHubAPI string
	Free      func(string) (uint64, error)
}

// New returns a Puller for the drive's current mode.
func New(d *drive.Drive, engineURL string) *Puller {
	return &Puller{Drive: d, Client: netguard.Client(d.Online()), EngineURL: engineURL, GitHubAPI: "https://api.github.com", Free: sys.FreeBytes}
}

// Pull fetches spec, or a catalogue id, and returns the model's name in the engine.
func (p *Puller) Pull(ctx context.Context, spec, name string, progress func(Event)) (string, error) {
	if e, ok := Lookup(spec); ok {
		spec = e.Spec
	}
	s, err := Parse(spec)
	if err != nil {
		return "", err
	}
	if s.Kind == "file" {
		return p.importFile(ctx, s.Path, name, false, progress)
	}
	if !p.Drive.Online() {
		return "", netguard.ErrOffline
	}
	if ref, ok := s.EngineRef(); ok {
		if name != "" {
			if err := p.enginePull(ctx, ref, progress); err != nil {
				return "", err
			}
			if err := p.engineJSON(ctx, "/api/copy", map[string]string{"source": ref, "destination": name}); err != nil {
				return "", err
			}
			return name, p.engineDelete(ctx, ref)
		}
		return ref, p.enginePull(ctx, ref, progress)
	}
	asset, base, err := p.resolve(ctx, s)
	if err != nil {
		return "", err
	}
	if asset.Size > 0 {
		// The engine stores its own copy before the download is removed, so both exist for a moment.
		if free, err := p.Free(p.Drive.Root); err == nil && free < uint64(asset.Size)*2+100_000_000 {
			return "", fmt.Errorf("%s needs %s free for the import, and the drive has %s", asset.Name, sys.GB(uint64(asset.Size)*2+100_000_000), sys.GB(free))
		}
	}
	file, err := p.download(ctx, asset, progress)
	if err != nil {
		return "", err
	}
	if name == "" {
		name = LocalName(base, filepath.Base(asset.Name))
	}
	return p.importFile(ctx, file, name, true, progress)
}

func (p *Puller) resolve(ctx context.Context, s Source) (Asset, string, error) {
	switch s.Kind {
	case "url":
		u, err := url.Parse(s.URL)
		if err != nil {
			return Asset{}, "", err
		}
		base := path.Base(u.Path)
		if !strings.HasSuffix(strings.ToLower(base), ".gguf") {
			return Asset{}, "", fmt.Errorf("%s isn't a .gguf link", s.URL)
		}
		return Asset{Name: base, URL: s.URL}, base, nil
	case "hf":
		rev := s.Ref
		if rev == "" {
			rev = "main"
		}
		assets, err := p.hfAssets(ctx, s.Repo, rev)
		if err != nil {
			return Asset{}, "", err
		}
		a, err := Choose(assets, s.Pattern)
		return a, strings.Split(s.Repo, "/")[1], err
	case "gh":
		assets, err := p.ghAssets(ctx, s.Repo, s.Ref)
		if err != nil {
			return Asset{}, "", err
		}
		a, err := Choose(assets, s.Pattern)
		return a, strings.Split(s.Repo, "/")[1], err
	}
	return Asset{}, "", fmt.Errorf("can't download a %s source", s.Kind)
}

func (p *Puller) getJSON(ctx context.Context, url string, headers map[string]string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", "usb-llms")
	res, err := p.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		io.Copy(io.Discard, res.Body)
		if res.StatusCode == 403 && res.Header.Get("X-Ratelimit-Remaining") == "0" {
			return res.StatusCode, fmt.Errorf("GitHub's limit is 60 requests an hour without a token; set GITHUB_TOKEN and try again")
		}
		return res.StatusCode, nil
	}
	return res.StatusCode, json.NewDecoder(res.Body).Decode(out)
}

func (p *Puller) hfAssets(ctx context.Context, repo, rev string) ([]Asset, error) {
	var info struct {
		Siblings []struct {
			RFilename string `json:"rfilename"`
			Size      int64  `json:"size"`
		} `json:"siblings"`
	}
	headers := map[string]string{}
	if t := os.Getenv("HF_TOKEN"); t != "" {
		headers["Authorization"] = "Bearer " + t
	}
	api := fmt.Sprintf("https://huggingface.co/api/models/%s/revision/%s?blobs=true", repo, url.PathEscape(rev))
	code, err := p.getJSON(ctx, api, headers, &info)
	if err != nil {
		return nil, err
	}
	if code >= 400 {
		return nil, fmt.Errorf("Hugging Face returned %d for %s", code, repo)
	}
	out := make([]Asset, 0, len(info.Siblings))
	for _, f := range info.Siblings {
		out = append(out, Asset{Name: f.RFilename, Size: f.Size, URL: fmt.Sprintf("https://huggingface.co/%s/resolve/%s/%s", repo, url.PathEscape(rev), f.RFilename)})
	}
	return out, nil
}

func (p *Puller) ghAssets(ctx context.Context, repo, tag string) ([]Asset, error) {
	headers := map[string]string{"Accept": "application/vnd.github+json"}
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		headers["Authorization"] = "Bearer " + t
	}
	var rel struct {
		Assets []struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	endpoint := p.GitHubAPI + "/repos/" + repo + "/releases/latest"
	if tag != "" {
		endpoint = p.GitHubAPI + "/repos/" + repo + "/releases/tags/" + url.PathEscape(tag)
	}
	code, err := p.getJSON(ctx, endpoint, headers, &rel)
	if err != nil {
		return nil, err
	}
	var out []Asset
	if code < 400 {
		for _, a := range rel.Assets {
			out = append(out, Asset{Name: a.Name, URL: a.URL, Size: a.Size, SHA256: strings.TrimPrefix(a.Digest, "sha256:")})
		}
	}
	for _, a := range out {
		if strings.HasSuffix(strings.ToLower(a.Name), ".gguf") {
			return out, nil
		}
	}
	// No GGUF in a release: look in the repo itself, where big files live in Git LFS.
	ref := tag
	if ref == "" {
		ref = "HEAD"
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			Size int64  `json:"size"`
		} `json:"tree"`
	}
	code, err = p.getJSON(ctx, p.GitHubAPI+"/repos/"+repo+"/git/trees/"+url.PathEscape(ref)+"?recursive=1", headers, &tree)
	if err != nil {
		return nil, err
	}
	if code >= 400 {
		return nil, fmt.Errorf("GitHub has no repo or ref %s@%s", repo, ref)
	}
	out = nil
	for _, e := range tree.Tree {
		if e.Type == "blob" {
			out = append(out, Asset{
				Name:   e.Path,
				URL:    "https://raw.githubusercontent.com/" + repo + "/" + ref + "/" + e.Path,
				LFSURL: "https://media.githubusercontent.com/media/" + repo + "/" + ref + "/" + e.Path,
			})
		}
	}
	return out, nil
}

// download keeps the file in data/tmp, resuming a partial copy, and checks it really is a GGUF.
func (p *Puller) download(ctx context.Context, a Asset, progress func(Event)) (string, error) {
	sum := sha256.Sum256([]byte(a.URL))
	part := filepath.Join(p.Drive.Tmp(), hex.EncodeToString(sum[:8])+".gguf.part")
	status := "downloading " + filepath.Base(a.Name)
	report := func(done, total int64) {
		if progress != nil {
			progress(Event{Status: status, Done: done, Total: total})
		}
	}
	if err := engine.Download(ctx, p.Client, a.URL, part, report); err != nil {
		return "", err
	}
	magic := make([]byte, 64)
	f, err := os.Open(part)
	if err != nil {
		return "", err
	}
	n, _ := io.ReadFull(f, magic)
	f.Close()
	magic = magic[:n]
	if a.LFSURL != "" && bytes.HasPrefix(magic, []byte("version https://git-lfs")) {
		os.Remove(part)
		if err := engine.Download(ctx, p.Client, a.LFSURL, part, report); err != nil {
			return "", err
		}
		if f, err = os.Open(part); err != nil {
			return "", err
		}
		n, _ = io.ReadFull(f, magic)
		f.Close()
		magic = magic[:n]
	}
	if !bytes.HasPrefix(magic, []byte("GGUF")) {
		os.Remove(part)
		return "", fmt.Errorf("%s isn't a GGUF file", a.Name)
	}
	if a.SHA256 != "" {
		got, err := fileHash(part, nil)
		if err != nil {
			return "", err
		}
		if got != a.SHA256 {
			os.Remove(part)
			return "", fmt.Errorf("%s failed its checksum", a.Name)
		}
	}
	return part, nil
}

func fileHash(path string, progress func(int64)) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	var done int64
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			done += int64(n)
			if progress != nil {
				progress(done)
			}
		}
		if err == io.EOF {
			return hex.EncodeToString(h.Sum(nil)), nil
		}
		if err != nil {
			return "", err
		}
	}
}

// importFile copies a GGUF into the engine's store. A downloaded source is deleted
// afterwards so the model doesn't take up twice the space; a file the user pointed at stays.
func (p *Puller) importFile(ctx context.Context, file, name string, owned bool, progress func(Event)) (string, error) {
	if name == "" {
		name = LocalName(filepath.Base(file), filepath.Base(file))
	}
	if progress != nil {
		progress(Event{Status: "checking the file"})
	}
	digest, err := fileHash(file, nil)
	if err != nil {
		return "", err
	}
	digest = "sha256:" + digest
	if err := p.uploadBlob(ctx, file, digest, progress); err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{"model": name, "files": map[string]string{"model.gguf": digest}, "stream": true})
	if err := p.engineStream(ctx, "/api/create", body, progress); err != nil {
		return "", err
	}
	if owned {
		os.Remove(file)
	}
	if progress != nil {
		progress(Event{Status: "ready", Model: name})
	}
	return name, nil
}

func (p *Puller) uploadBlob(ctx context.Context, file, digest string, progress func(Event)) error {
	url := p.EngineURL + "/api/blobs/" + digest
	head, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return err
	}
	res, err := netguard.Local().Do(head)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode == 200 {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if progress != nil {
		progress(Event{Status: "copying into the engine", Total: st.Size()})
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, f)
	if err != nil {
		return err
	}
	req.ContentLength = st.Size()
	res, err = netguard.Local().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
		return fmt.Errorf("the engine refused the file: %s", strings.TrimSpace(string(msg)))
	}
	return nil
}

func (p *Puller) enginePull(ctx context.Context, ref string, progress func(Event)) error {
	body, _ := json.Marshal(map[string]any{"model": ref, "stream": true})
	if err := p.engineStream(ctx, "/api/pull", body, progress); err != nil {
		return err
	}
	if progress != nil {
		progress(Event{Status: "ready", Model: ref})
	}
	return nil
}

func (p *Puller) engineStream(ctx context.Context, route string, body []byte, progress func(Event)) error {
	req, err := http.NewRequestWithContext(ctx, "POST", p.EngineURL+route, bytes.NewReader(body))
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
			Status    string `json:"status"`
			Error     string `json:"error"`
			Completed int64  `json:"completed"`
			Total     int64  `json:"total"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Error != "" {
			return fmt.Errorf("engine: %s", ev.Error)
		}
		if progress != nil {
			progress(Event{Status: ev.Status, Done: ev.Completed, Total: ev.Total})
		}
	}
	if res.StatusCode >= 400 {
		return fmt.Errorf("engine returned HTTP %d for %s", res.StatusCode, route)
	}
	return sc.Err()
}

func (p *Puller) engineJSON(ctx context.Context, route string, body any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", p.EngineURL+route, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	res, err := netguard.Local().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
		return fmt.Errorf("engine: %s", strings.TrimSpace(string(msg)))
	}
	return nil
}

func (p *Puller) engineDelete(ctx context.Context, name string) error {
	return Remove(ctx, p.EngineURL, name)
}

// Remove deletes a model from the engine.
func Remove(ctx context.Context, engineURL, name string) error {
	raw, _ := json.Marshal(map[string]string{"model": name})
	req, err := http.NewRequestWithContext(ctx, "DELETE", engineURL+"/api/delete", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	res, err := netguard.Local().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
		return fmt.Errorf("couldn't delete %s: %s", name, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Installed is a model in the engine.
type Installed struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// List returns the models in the engine.
func List(ctx context.Context, engineURL string) ([]Installed, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", engineURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	res, err := netguard.Local().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var out struct {
		Models []Installed `json:"models"`
	}
	return out.Models, json.NewDecoder(res.Body).Decode(&out)
}
