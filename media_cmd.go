package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Gitguy268/usb-llms/internal/drive"
	"github.com/Gitguy268/usb-llms/internal/media"
	"github.com/Gitguy268/usb-llms/internal/netguard"
	"github.com/Gitguy268/usb-llms/internal/sys"
)

const mediaUsage = `usb-llms media <command>

  ls                       what can be made and what is on the drive
  install KIND|sd          set up a kind with its default model: image, video, speech,
                           transcribe, listen, 3d, music (sd = just the sd-server engine)
  pull ID                  put one catalogue model on the drive
  rm ID                    remove it
  use KIND ID              make ID the default for KIND
  out [N]                  the newest things made
  stop                     stop the image/video, 3D and music engines
  api REMOTE [image=M] [speech=M] [voice=V] [transcribe=M]
                           use a remote (usb-llms remote add) for kinds the drive can't make
  api off                  stop using it

usb-llms make image|video|speech|music|3d "PROMPT" [--image FILE] [--model ID] [-o FILE]
         [--width N --height N --steps N --seed N --frames N --voice V --seconds N --lyrics FILE]
usb-llms transcribe AUDIO-FILE [--language xx] [--translate] [-o FILE]
`

func printPull(e media.PullEvent) {
	if e.Total > 0 {
		fmt.Printf("\r%s: %s of %s   ", e.Status, sys.GB(uint64(e.Done)), sys.GB(uint64(e.Total)))
	} else {
		fmt.Printf("\n%s", e.Status)
	}
}

func mediaCmd(d *drive.Drive, args []string) error {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	ctx, cancel := interrupted()
	defer cancel()
	switch sub {
	case "", "ls":
		fmt.Printf("%-2s %-17s %-19s %8s  %s\n", "", "ID", "KINDS", "SIZE", "LICENCE")
		for _, r := range media.Status(ctx, d) {
			mark := "○"
			if r.Ready {
				mark = "●"
			}
			def := ""
			if r.Default {
				def = "  (default)"
			}
			fmt.Printf("%-2s %-17s %-19s %5.2f GB  %s%s\n", mark, r.ID, strings.Join(r.Kinds, ","), r.SizeGB, r.Licence, def)
		}
		sd, ok := media.FindSD(d)
		if !ok {
			sd = "not on the drive for " + drive.Platform()
		}
		fmt.Printf("\nsd-server: %s\nresults:   %s\n", sd, d.MediaOut())
		return nil
	case "install", "pull":
		if len(args) == 0 {
			return fmt.Errorf("usb-llms media %s %s", sub, map[string]string{"install": "KIND|sd", "pull": "ID"}[sub])
		}
		for _, what := range args {
			id := what
			if what == "sd" {
				fmt.Println("Installing stable-diffusion.cpp…")
				bin, err := media.InstallSD(ctx, d, netguard.Client(d.Online()), os.Getenv("USB_LLMS_SD_VARIANT"), func(done, total int64) {
					printPull(media.PullEvent{Status: "sd-server", Done: done, Total: total})
				})
				fmt.Println()
				if err != nil {
					return err
				}
				fmt.Println("Installed", bin)
				continue
			}
			if sub == "install" {
				e, ok := media.DefaultFor(d, what)
				if !ok {
					return fmt.Errorf("unknown kind %q; kinds: %s, or sd", what, strings.Join(media.Kinds, ", "))
				}
				id = e.ID
				fmt.Printf("Getting %s (%.1f GB, %s)…\n", e.Name, e.SizeGB, e.Licence)
			}
			err := media.Pull(ctx, d, id, printPull, func(line string) { fmt.Println(line) })
			fmt.Println()
			if err != nil {
				return err
			}
			fmt.Println("Ready:", id)
		}
		return nil
	case "rm":
		if len(args) == 0 {
			return errors.New("usb-llms media rm ID")
		}
		if args[0] == "sd" {
			media.StopSD(d)
			return os.RemoveAll(filepath.Join(d.Engine(), "sd"))
		}
		return media.RemoveModel(ctx, d, args[0])
	case "use":
		if len(args) != 2 {
			return errors.New("usb-llms media use KIND ID")
		}
		if _, ok := media.Find(d, args[1]); !ok {
			return fmt.Errorf("no media model %q", args[1])
		}
		cfg, err := d.LoadConfig()
		if err != nil {
			return err
		}
		if cfg.Media.Defaults == nil {
			cfg.Media.Defaults = map[string]string{}
		}
		cfg.Media.Defaults[args[0]] = args[1]
		return d.SaveConfig(cfg)
	case "out":
		n := 10
		if len(args) > 0 {
			fmt.Sscan(args[0], &n)
		}
		for _, o := range media.List(d, n) {
			text := ""
			if o.Text != nil {
				text = fmt.Sprintf("  %q", trunc(*o.Text, 60))
			}
			fmt.Printf("%-10s %s%s\n", o.Kind, o.Path, text)
		}
		return nil
	case "stop":
		stopped := []string{}
		if media.StopSD(d) {
			stopped = append(stopped, "sd-server")
		}
		for _, n := range []string{"3d", "music"} {
			if media.StopSidecar(d, n) {
				stopped = append(stopped, n)
			}
		}
		if len(stopped) == 0 {
			fmt.Println("Nothing was running.")
		} else {
			fmt.Println("Stopped", strings.Join(stopped, ", "))
		}
		return nil
	case "api":
		cfg, err := d.LoadConfig()
		if err != nil {
			return err
		}
		if len(args) == 0 {
			fmt.Printf("remote: %q  image: %q  speech: %q  voice: %q  transcribe: %q\n", cfg.Media.API, cfg.Media.Image, cfg.Media.Speech, cfg.Media.Voice, cfg.Media.Transcribe)
			return nil
		}
		if args[0] == "off" {
			cfg.Media.API, cfg.Media.Image, cfg.Media.Speech, cfg.Media.Voice, cfg.Media.Transcribe = "", "", "", "", ""
			return d.SaveConfig(cfg)
		}
		found := false
		for _, r := range cfg.Remotes {
			found = found || r.Name == args[0]
		}
		if !found {
			return fmt.Errorf("no remote %q; add it first with usb-llms remote add", args[0])
		}
		cfg.Media.API = args[0]
		for _, kv := range args[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("expected key=value, got %q", kv)
			}
			switch k {
			case "image":
				cfg.Media.Image = v
			case "speech":
				cfg.Media.Speech = v
			case "voice":
				cfg.Media.Voice = v
			case "transcribe":
				cfg.Media.Transcribe = v
			default:
				return fmt.Errorf("unknown key %q; use image, speech, voice or transcribe", k)
			}
		}
		return d.SaveConfig(cfg)
	case "help":
		fmt.Print(mediaUsage)
		return nil
	}
	return fmt.Errorf("unknown media command %q; run usb-llms media help", sub)
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// makeFlags parses flags that may come before or after the prompt words.
func makeFlags(name string, args []string) (*flag.FlagSet, map[string]*string, []string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	vals := map[string]*string{}
	for _, k := range []string{"image", "model", "o", "width", "height", "steps", "seed", "frames", "voice", "seconds", "lyrics", "negative", "language"} {
		vals[k] = fs.String(k, "", "")
	}
	translate := fs.Bool("translate", false, "")
	words := []string{}
	rest := args
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return nil, nil, nil, err
		}
		rest = fs.Args()
		if len(rest) > 0 {
			words = append(words, rest[0])
			rest = rest[1:]
		}
	}
	if *translate {
		t := "true"
		vals["translate"] = &t
	}
	return fs, vals, words, nil
}

func atoi(s string) int {
	n := 0
	fmt.Sscan(s, &n)
	return n
}

func runMake(d *drive.Drive, req media.Request, out string) error {
	ctx, cancel := interrupted()
	defer cancel()
	last := ""
	res, err := media.Make(ctx, d, req, func(p media.Progress) {
		line := p.Status
		if p.Total > 0 {
			line = fmt.Sprintf("%s %d/%d", p.Status, p.Step, p.Total)
		}
		if line != last {
			fmt.Printf("\r\033[2K%s", line)
			last = line
		}
	})
	fmt.Print("\r\033[2K")
	if err != nil {
		return err
	}
	if res.Text != nil {
		fmt.Println(*res.Text)
	}
	path := res.Path
	if out != "" {
		raw, err := os.ReadFile(res.Path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, raw, 0o644); err != nil {
			return err
		}
		path = out
	}
	fmt.Println(path)
	return nil
}

func makeCmd(d *drive.Drive, args []string) error {
	if len(args) == 0 {
		return errors.New(`usb-llms make image|video|speech|music|3d "PROMPT" (see usb-llms media help)`)
	}
	kind := args[0]
	switch kind {
	case "image", "video", "speech", "music", "3d":
	default:
		return fmt.Errorf("can't make %q; choose image, video, speech, music or 3d", kind)
	}
	_, v, words, err := makeFlags("make", args[1:])
	if err != nil {
		return err
	}
	req := media.Request{Kind: kind, Prompt: strings.Join(words, " "), Model: *v["model"], Negative: *v["negative"], Voice: *v["voice"],
		Width: atoi(*v["width"]), Height: atoi(*v["height"]), Steps: atoi(*v["steps"]), Frames: atoi(*v["frames"]), Seconds: atoi(*v["seconds"])}
	if *v["seed"] != "" {
		s := int64(atoi(*v["seed"]))
		req.Seed = &s
	}
	if *v["image"] != "" {
		if req.Image, err = os.ReadFile(*v["image"]); err != nil {
			return err
		}
	}
	if *v["lyrics"] != "" {
		raw, err := os.ReadFile(*v["lyrics"])
		if err != nil {
			return err
		}
		req.Lyrics = string(raw)
	}
	return runMake(d, req, *v["o"])
}

func transcribeCmd(d *drive.Drive, args []string) error {
	_, v, words, err := makeFlags("transcribe", args)
	if err != nil {
		return err
	}
	if len(words) == 0 {
		return errors.New("usb-llms transcribe AUDIO-FILE [--language xx] [--translate]")
	}
	audio, err := os.ReadFile(words[0])
	if err != nil {
		return err
	}
	req := media.Request{Kind: "transcribe", Audio: audio, Model: *v["model"], Language: *v["language"]}
	if t, ok := v["translate"]; ok && *t == "true" {
		req.Translate = true
	}
	return runMake(d, req, *v["o"])
}
