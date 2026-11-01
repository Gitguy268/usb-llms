// Package models turns a model spec into a model in the drive's engine.
package models

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Source is a parsed model spec. The syntax matches LvW-harness, so one spec works in both tools:
//
//	hf:owner/repo[@revision][:pattern]   Hugging Face (pattern = a quant like Q4_K_M, or a file)
//	gh:owner/repo[@tag][:pattern]        GitHub release assets, falling back to repo files
//	https://host/path/model.gguf         any direct URL
//	ollama:name[:tag]                    the Ollama library
//	./model.gguf                         a local file
//
// A bare owner/repo means Hugging Face and a bare name means the Ollama library.
type Source struct {
	Kind    string // hf, gh, url, ollama or file
	Repo    string
	Ref     string
	Pattern string
	URL     string
	Name    string
	Path    string
}

var repoSpec = regexp.MustCompile(`^([^@:\s]+?)(?:@([^:\s]+))?(?::(.+))?$`)

func parseRepo(rest, kind string) (Source, error) {
	m := repoSpec.FindStringSubmatch(rest)
	if m == nil {
		return Source{}, fmt.Errorf("bad %s spec %q; expected owner/repo[@ref][:pattern]", kind, rest)
	}
	repo := strings.TrimRight(m[1], "/")
	if parts := strings.Split(repo, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Source{}, fmt.Errorf("%q should be owner/repo", repo)
	}
	return Source{Kind: kind, Repo: repo, Ref: m[2], Pattern: m[3]}, nil
}

// Parse reads a spec.
func Parse(input string) (Source, error) {
	spec := strings.TrimSpace(input)
	switch {
	case spec == "":
		return Source{}, fmt.Errorf("no model given")
	case strings.HasPrefix(spec, "ollama:"):
		name := strings.TrimPrefix(spec, "ollama:")
		if name == "" {
			return Source{}, fmt.Errorf("ollama: needs a model name, e.g. ollama:llama3.2")
		}
		return Source{Kind: "ollama", Name: name}, nil
	case strings.HasPrefix(spec, "gl:"):
		return Source{}, fmt.Errorf("GitLab specs aren't supported here; use the file's direct URL")
	case strings.HasPrefix(spec, "http://"), strings.HasPrefix(spec, "https://"):
		return fromURL(spec)
	case strings.HasPrefix(spec, "hf:"):
		return parseRepo(spec[3:], "hf")
	case strings.HasPrefix(spec, "gh:"):
		return parseRepo(spec[3:], "gh")
	case strings.HasPrefix(spec, "hf.co/"):
		return parseRepo(spec[len("hf.co/"):], "hf")
	}
	if strings.HasSuffix(strings.ToLower(spec), ".gguf") {
		if _, err := os.Stat(spec); err != nil {
			return Source{}, fmt.Errorf("no such file: %s", spec)
		}
		abs, err := filepath.Abs(spec)
		if err != nil {
			return Source{}, err
		}
		return Source{Kind: "file", Path: abs}, nil
	}
	if strings.Contains(spec, "/") {
		return parseRepo(spec, "hf")
	}
	return Source{Kind: "ollama", Name: spec}, nil
}

func fromURL(spec string) (Source, error) {
	u, err := url.Parse(spec)
	if err != nil {
		return Source{}, err
	}
	var parts []string
	for _, p := range strings.Split(u.Path, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	switch {
	case (u.Host == "huggingface.co" || u.Host == "hf.co") && len(parts) >= 2 && parts[0] != "api" && parts[0] != "datasets" && parts[0] != "spaces":
		repo := parts[0] + "/" + parts[1]
		if len(parts) >= 5 && (parts[2] == "resolve" || parts[2] == "blob") {
			return Source{Kind: "hf", Repo: repo, Ref: parts[3], Pattern: strings.Join(parts[4:], "/")}, nil
		}
		if len(parts) >= 4 && parts[2] == "tree" {
			return Source{Kind: "hf", Repo: repo, Ref: parts[3]}, nil
		}
		return Source{Kind: "hf", Repo: repo}, nil
	case u.Host == "github.com" && len(parts) >= 2:
		if len(parts) >= 4 && parts[2] == "releases" && parts[3] == "download" {
			return Source{Kind: "url", URL: spec}, nil
		}
		s := Source{Kind: "gh", Repo: parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")}
		if len(parts) >= 5 && parts[2] == "releases" && parts[3] == "tag" {
			s.Ref = parts[4]
		}
		return s, nil
	}
	return Source{Kind: "url", URL: spec}, nil
}

// EngineRef is the name Ollama pulls natively, for sources that don't need our downloader.
// A pattern naming one file or a glob can't be expressed as an Ollama tag, so it goes through ours.
func (s Source) EngineRef() (string, bool) {
	switch s.Kind {
	case "ollama":
		return s.Name, true
	case "hf":
		if s.Ref != "" && s.Ref != "main" {
			return "", false
		}
		if s.Pattern == "" {
			return "hf.co/" + s.Repo, true
		}
		if isPlainTag(s.Pattern) {
			return "hf.co/" + s.Repo + ":" + s.Pattern, true
		}
	}
	return "", false
}

func isPlainTag(p string) bool {
	return !strings.ContainsAny(p, "*?/") && !strings.HasSuffix(strings.ToLower(p), ".gguf")
}

var quantInName = regexp.MustCompile(`(?i)(?:^|[-._])(i?q\d(?:_[a-z0-9]+)*|bf16|f16|f32)(?:[-._]|$)`)
var preferredQuants = []string{"q4_k_m", "q4_k_s", "q4_0", "iq4_xs", "q5_k_m", "q6_k", "q8_0"}
var unsafeName = regexp.MustCompile(`[^a-z0-9._-]+`)

// LocalName turns a repo or file name into an Ollama model name, e.g. aimodel1:q4_k_m.
func LocalName(base, file string) string {
	name := strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base)))
	if strings.HasSuffix(strings.ToLower(base), ".gguf") {
		base = name
	} else {
		name = strings.ToLower(base)
	}
	quant := ""
	if m := quantInName.FindStringSubmatch(file); m != nil {
		quant = strings.ToLower(m[1])
		name = strings.TrimRight(strings.TrimSuffix(name, quant), "-._")
	}
	name = strings.Trim(unsafeName.ReplaceAllString(strings.TrimSuffix(name, "-gguf"), "-"), "-._")
	if name == "" {
		name = "model"
	}
	if quant != "" {
		return name + ":" + quant
	}
	return name
}

// Asset is a file a source offers.
type Asset struct {
	Name   string
	URL    string
	LFSURL string
	Size   int64
	SHA256 string
}

var shard = regexp.MustCompile(`(?i)-\d{5}-of-\d{5}\.gguf$`)

// Choose picks the GGUF to download. Without a pattern it prefers Q4_K_M, as LvW-harness does.
// Split GGUFs aren't supported because a single import needs a single file.
func Choose(assets []Asset, pattern string) (Asset, error) {
	var ggufs []Asset
	for _, a := range assets {
		lower := strings.ToLower(filepath.Base(a.Name))
		if strings.HasSuffix(lower, ".gguf") && !strings.HasPrefix(lower, "mmproj") {
			ggufs = append(ggufs, a)
		}
	}
	if len(ggufs) == 0 {
		return Asset{}, fmt.Errorf("no .gguf file found; USB LLMS can only run GGUF models")
	}
	pick := func(a Asset) (Asset, error) {
		if shard.MatchString(a.Name) {
			return Asset{}, fmt.Errorf("%s is split into parts, which isn't supported; pick a single-file version", a.Name)
		}
		return a, nil
	}
	if pattern != "" {
		for _, a := range ggufs {
			if matchPattern(a.Name, pattern) {
				return pick(a)
			}
		}
		names := make([]string, 0, len(ggufs))
		for _, a := range ggufs {
			names = append(names, filepath.Base(a.Name))
		}
		return Asset{}, fmt.Errorf("nothing matches %q. Files: %s", pattern, strings.Join(names, ", "))
	}
	for _, q := range preferredQuants {
		for _, a := range ggufs {
			if matchPattern(a.Name, q) {
				return pick(a)
			}
		}
	}
	if len(ggufs) > 1 {
		names := make([]string, 0, len(ggufs))
		for _, a := range ggufs {
			names = append(names, filepath.Base(a.Name))
		}
		return Asset{}, fmt.Errorf("several files and no way to choose; add :pattern. Files: %s", strings.Join(names, ", "))
	}
	return pick(ggufs[0])
}

func matchPattern(name, pattern string) bool {
	base := filepath.Base(name)
	if strings.ContainsAny(pattern, "*?") {
		ok, _ := filepath.Match(strings.ToLower(pattern), strings.ToLower(base))
		return ok
	}
	if name == pattern || base == pattern {
		return true
	}
	re, err := regexp.Compile(`(^|[-._/])` + regexp.QuoteMeta(strings.ToLower(pattern)) + `([-.]|$)`)
	return err == nil && re.MatchString(strings.ToLower(base))
}
