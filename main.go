// Command usb-llms runs local AI models from a portable folder on macOS, Windows and Linux.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/Gitguy268/usb-llms/internal/drive"
	"github.com/Gitguy268/usb-llms/internal/engine"
	"github.com/Gitguy268/usb-llms/internal/lvw"
	"github.com/Gitguy268/usb-llms/internal/models"
	"github.com/Gitguy268/usb-llms/internal/netguard"
	"github.com/Gitguy268/usb-llms/internal/sys"
	"github.com/Gitguy268/usb-llms/internal/tui"
	"github.com/Gitguy268/usb-llms/internal/web"
	"github.com/Gitguy268/usb-llms/internal/webui"
)

const version = "0.1.0"

const usage = `USB LLMS runs AI models from this folder on macOS, Windows and Linux.

Start here:
  usb-llms                     open the terminal interface
  usb-llms setup [--lean|--full] [--model ID]
                               add the engine for this computer, and a model if you name one
  usb-llms web [--lan] [--port N] [--no-open]
                               open the web interface
  usb-llms app                 open the chat in its own window

Models:
  usb-llms catalog             list suggested models
  usb-llms pull SPEC [--name N]
                               add a model. SPEC is a catalogue id, hf:owner/repo[:quant],
                               gh:owner/repo[@tag][:pattern], a link to a .gguf, a file, or an Ollama name
  usb-llms run|list|ps|show|rm|cp|create ...
                               the same commands as Ollama, run against this drive

Modes:
  usb-llms mode [online|offline]
                               show or change the mode. Offline blocks all outside connections
  usb-llms remote add NAME URL [KEY_ENV] | remote ls | remote rm NAME
                               OpenAI-compatible services for online mode. Only the name of the
                               key's environment variable is stored, never the key

The engine:
  usb-llms serve               run the engine in this terminal until Ctrl+C
  usb-llms start | stop | status

Other interfaces:
  usb-llms webui install|start [--no-open]
                               Open WebUI, installed onto this drive (about 4 GB)
  usb-llms lvw install [PATH]  copy an LvW-harness binary onto this drive
  usb-llms lvw [ARGS]          run LvW-harness against this drive

Add --home DIR before any command to use another folder.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "usb-llms:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	home := ""
	if len(args) >= 2 && args[0] == "--home" {
		home, args = args[1], args[2:]
	}
	cmd := ""
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "help", "--help", "-h":
		fmt.Print(usage)
		return nil
	case "version", "--version", "-v":
		fmt.Printf("usb-llms %s (engine %s, %s)\n", version, engine.Version, drive.Platform())
		return nil
	}
	d, err := drive.Find(home)
	if err != nil {
		return err
	}
	switch cmd {
	case "":
		return runTUI(d)
	case "setup":
		return setup(d, args)
	case "start":
		st, err := engine.Ensure(d)
		if err != nil {
			return err
		}
		fmt.Println("The engine is running at", st.URL())
		return nil
	case "stop":
		if len(args) == 0 {
			return engine.Stop(d)
		}
		return passthrough(d, cmd, args)
	case "status":
		return status(d)
	case "serve":
		return serve(d)
	case "pull":
		return pull(d, args)
	case "catalog":
		return catalog()
	case "run", "list", "ls", "ps", "show", "rm", "cp", "create":
		return passthrough(d, cmd, args)
	case "mode":
		return mode(d, args)
	case "remote":
		return remote(d, args)
	case "web":
		return webCmd(d, args, false)
	case "app":
		return webCmd(d, args, true)
	case "webui":
		return webuiCmd(d, args)
	case "lvw":
		return lvwCmd(d, args)
	}
	return fmt.Errorf("unknown command %q; run usb-llms help", cmd)
}

func runTUI(d *drive.Drive) error {
	// An engine left running would hold the drive open and block ejecting it.
	if _, wasRunning := engine.Running(d); !wasRunning {
		defer func() { _ = engine.Stop(d) }()
	}
	action, err := tui.Run(d)
	if err != nil {
		return err
	}
	switch action {
	case tui.ActionWeb:
		return webCmd(d, nil, false)
	case tui.ActionApp:
		return webCmd(d, nil, true)
	case tui.ActionWebUI:
		return webuiCmd(d, []string{"start"})
	case tui.ActionLvw:
		return lvwCmd(d, nil)
	}
	return nil
}

func interrupted() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func setup(d *drive.Drive, args []string) error {
	variant, modelID := engine.Variant(""), ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--lean":
			variant = engine.Lean
		case "--full":
			variant = engine.Full
		case "--model":
			i++
			if i >= len(args) {
				return errors.New("--model needs a value")
			}
			modelID = args[i]
		default:
			return fmt.Errorf("unknown option %s", args[i])
		}
	}
	ctx, cancel := interrupted()
	defer cancel()
	if _, ok := engine.Installed(d); ok && variant == "" {
		fmt.Println("The engine for", drive.Platform(), "is already on this drive.")
	} else {
		if !d.Online() {
			return netguard.ErrOffline
		}
		if _, running := engine.Running(d); running {
			if err := engine.Stop(d); err != nil {
				return err
			}
		}
		fmt.Printf("Adding Ollama %s for %s to %s\n", engine.Version, drive.Platform(), d.Root)
		m, err := engine.Install(ctx, d, netguard.Client(true), engine.InstallOptions{Variant: variant, Progress: bar("Downloading the engine")})
		fmt.Println()
		if err != nil {
			return err
		}
		fmt.Printf("Engine ready (%s).\n", m.Variant)
	}
	if modelID != "" {
		return pull(d, []string{modelID})
	}
	if !d.HasModels() {
		fmt.Println("Next, add a model: usb-llms catalog, then usb-llms pull <id>")
	}
	return nil
}

func bar(label string) func(done, total int64) {
	last := time.Time{}
	return func(done, total int64) {
		if time.Since(last) < 200*time.Millisecond && done != total {
			return
		}
		last = time.Now()
		if total > 0 {
			fmt.Printf("\r%s: %s of %s   ", label, sys.GB(uint64(done)), sys.GB(uint64(total)))
		} else {
			fmt.Printf("\r%s: %s   ", label, sys.GB(uint64(done)))
		}
	}
}

func status(d *drive.Drive) error {
	mode := "offline"
	if d.Online() {
		mode = "online"
	}
	fmt.Println("Folder  ", d.Root)
	fmt.Println("Mode    ", mode)
	if m, ok := engine.Installed(d); ok {
		fmt.Printf("Engine   %s, %s\n", m.Version, m.Variant)
	} else {
		fmt.Println("Engine   not installed for", drive.Platform())
	}
	if st, ok := engine.Running(d); ok {
		fmt.Println("Running ", st.URL())
	} else {
		fmt.Println("Running  no")
	}
	if free, err := sys.FreeBytes(d.Root); err == nil {
		fmt.Println("Free    ", sys.GB(free))
	}
	if ram := sys.RAMBytes(); ram > 0 {
		fmt.Println("Memory  ", sys.GB(ram))
	}
	fmt.Println("WebUI   ", map[bool]string{true: "installed", false: "not installed"}[webui.Installed(d)])
	return nil
}

func serve(d *drive.Drive) error {
	st, err := engine.Ensure(d)
	if err != nil {
		return err
	}
	fmt.Printf("The engine is running at %s. Press Ctrl+C to stop it.\n", st.URL())
	ctx, cancel := interrupted()
	defer cancel()
	<-ctx.Done()
	return engine.Stop(d)
}

func pull(d *drive.Drive, args []string) error {
	spec, name := "", ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--name" && i+1 < len(args):
			i++
			name = args[i]
		case spec == "":
			spec = args[i]
		default:
			return fmt.Errorf("unexpected %q", args[i])
		}
	}
	if spec == "" {
		return errors.New("name a model to add; usb-llms catalog lists some")
	}
	if e, ok := models.Lookup(spec); ok {
		warnRAM(e)
	}
	st, err := engine.Ensure(d)
	if err != nil {
		return err
	}
	ctx, cancel := interrupted()
	defer cancel()
	last := ""
	got, err := models.New(d, st.URL()).Pull(ctx, spec, name, func(e models.Event) {
		if e.Total > 0 {
			fmt.Printf("\r%s: %s of %s   ", e.Status, sys.GB(uint64(e.Done)), sys.GB(uint64(e.Total)))
		} else if e.Status != last {
			fmt.Printf("\n%s", e.Status)
		}
		last = e.Status
	})
	fmt.Println()
	if err != nil {
		return err
	}
	fmt.Println("Added", got)
	fmt.Println("Chat with it: usb-llms run", got)
	return nil
}

func warnRAM(e models.Entry) {
	if ram := sys.RAMBytes(); ram > 0 && e.RAMGB*1e9 > float64(ram) {
		fmt.Printf("Warning: %s needs about %.0f GB of memory, and this computer has %s.\n", e.Name, e.RAMGB, sys.GB(ram))
	}
}

func catalog() error {
	fmt.Printf("%-24s %8s %8s  %-11s %s\n", "ID", "SIZE", "MEMORY", "LABEL", "LICENCE")
	for _, e := range models.Catalog() {
		fmt.Printf("%-24s %5.2f GB %5.0f GB  %-11s %s\n", e.ID, e.SizeGB, e.RAMGB, e.Label, e.Licence)
	}
	return nil
}

// passthrough runs the engine's own CLI against this drive, so the commands behave exactly like Ollama's.
func passthrough(d *drive.Drive, cmd string, args []string) error {
	st, err := engine.Ensure(d)
	if err != nil {
		return err
	}
	m, ok := engine.Installed(d)
	if !ok {
		return errors.New("no engine on this drive")
	}
	c := exec.Command(filepath.Join(d.Engine(), m.Binary), append([]string{cmd}, args...)...)
	c.Env = engine.Env(os.Environ(), d, st.Port, st.Online)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return childExit(c.Run())
}

func childExit(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	return err
}

func mode(d *drive.Drive, args []string) error {
	if len(args) > 0 {
		if args[0] != "online" && args[0] != "offline" {
			return errors.New("the mode is online or offline")
		}
		if err := web.SetMode(d, args[0] == "online"); err != nil {
			return err
		}
	}
	if d.Online() {
		fmt.Println("Online: downloads and remote services are allowed.")
	} else {
		fmt.Println("Offline: nothing leaves this computer.")
	}
	return nil
}

func remote(d *drive.Drive, args []string) error {
	cfg, err := d.LoadConfig()
	if err != nil {
		return err
	}
	sub := "ls"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "ls":
		if len(cfg.Remotes) == 0 {
			fmt.Println("No remotes yet. Add one: usb-llms remote add NAME URL [KEY_ENV]")
		}
		for _, r := range cfg.Remotes {
			fmt.Printf("%-16s %s  %s\n", r.Name, r.URL, r.KeyEnv)
		}
		return nil
	case "add":
		if len(args) < 2 {
			return errors.New("usage: usb-llms remote add NAME URL [KEY_ENV]")
		}
		r := drive.Remote{Name: args[0], URL: strings.TrimRight(args[1], "/")}
		if strings.ContainsAny(r.Name, "/ ") {
			return errors.New("a remote's name can't contain slashes or spaces")
		}
		if len(args) > 2 {
			r.KeyEnv = args[2]
			if strings.ContainsAny(r.KeyEnv, " /-.") || strings.HasPrefix(r.KeyEnv, "sk") {
				return errors.New("give the name of an environment variable, not the key itself")
			}
		}
		keep := cfg.Remotes[:0]
		for _, x := range cfg.Remotes {
			if x.Name != r.Name {
				keep = append(keep, x)
			}
		}
		cfg.Remotes = append(keep, r)
	case "rm":
		if len(args) != 1 {
			return errors.New("usage: usb-llms remote rm NAME")
		}
		keep := cfg.Remotes[:0]
		for _, x := range cfg.Remotes {
			if x.Name != args[0] {
				keep = append(keep, x)
			}
		}
		cfg.Remotes = keep
	default:
		return fmt.Errorf("unknown remote command %q", sub)
	}
	return d.SaveConfig(cfg)
}

func webCmd(d *drive.Drive, args []string, appWindow bool) error {
	port, lan, open := web.DefaultPort, false, true
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--lan":
			lan = true
		case "--no-open":
			open = false
		case "--port":
			i++
			if i >= len(args) || func() bool { _, err := fmt.Sscan(args[i], &port); return err != nil }() {
				return errors.New("--port needs a number")
			}
		default:
			return fmt.Errorf("unknown option %s", args[i])
		}
	}
	_, wasRunning := engine.Running(d)
	if _, ok := engine.Installed(d); ok {
		if _, err := engine.Ensure(d); err != nil {
			return err
		}
	}
	s := &web.Server{Drive: d}
	if lan {
		tok, err := web.NewToken()
		if err != nil {
			return err
		}
		s.Token = tok
	}
	l, port, err := web.Listen(port, lan)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if lan {
		url = fmt.Sprintf("http://127.0.0.1:%d/?token=%s", port, s.Token)
		ip := web.LANAddress()
		if ip == "" {
			fmt.Println("No local network address found.")
		} else {
			phone := fmt.Sprintf("http://%s:%d/?token=%s", ip, port, s.Token)
			fmt.Println("On another device on this network, open:", phone)
			if q, err := qrcode.New(phone, qrcode.Low); err == nil {
				fmt.Println(q.ToSmallString(false))
			}
			fmt.Println("Traffic on the network isn't encrypted. Use this only on a network you trust.")
		}
	}
	fmt.Println("USB LLMS is at", url)
	fmt.Println("Press Ctrl+C to stop.")
	ctx, cancel := interrupted()
	defer cancel()
	go func() { _ = srv.Serve(l) }()
	var window *exec.Cmd
	cleanup := func() {}
	if open && appWindow {
		window, cleanup = openAppWindow(url)
		if window != nil {
			go func() {
				_ = window.Wait()
				cancel()
			}()
		}
	}
	if open && window == nil {
		openBrowser(url)
	}
	<-ctx.Done()
	if window != nil && window.Process != nil {
		_ = window.Process.Kill()
	}
	cleanup()
	shut, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	_ = srv.Shutdown(shut)
	if !wasRunning {
		return engine.Stop(d)
	}
	return nil
}

func browserPaths() []string {
	if p := os.Getenv("USB_LLMS_BROWSER"); p != "" {
		return []string{p}
	}
	switch runtime.GOOS {
	case "darwin":
		var out []string
		for _, app := range []string{"Google Chrome", "Microsoft Edge", "Brave Browser", "Chromium"} {
			out = append(out, "/Applications/"+app+".app/Contents/MacOS/"+app)
		}
		return out
	case "windows":
		var out []string
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if root != "" {
				out = append(out,
					filepath.Join(root, `Google\Chrome\Application\chrome.exe`),
					filepath.Join(root, `Microsoft\Edge\Application\msedge.exe`),
					filepath.Join(root, `BraveSoftware\Brave-Browser\Application\brave.exe`))
			}
		}
		return out
	}
	var out []string
	for _, name := range []string{"google-chrome", "google-chrome-stable", "microsoft-edge", "brave-browser", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// openAppWindow starts a Chromium-family browser in app mode. Its profile sits in the host's temp
// folder because browsers need symlinks that exFAT lacks, and is deleted when the window closes.
func openAppWindow(url string) (*exec.Cmd, func()) {
	for _, p := range browserPaths() {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		profile, err := os.MkdirTemp("", "usb-llms-window-")
		if err != nil {
			return nil, func() {}
		}
		cmd := exec.Command(p, "--app="+url, "--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check", "--window-size=1040,760")
		if err := cmd.Start(); err != nil {
			os.RemoveAll(profile)
			continue
		}
		return cmd, func() { os.RemoveAll(profile) }
	}
	return nil, func() {}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Println("Open this link in your browser:", url)
		return
	}
	go func() { _ = cmd.Wait() }()
}

func webuiCmd(d *drive.Drive, args []string) error {
	sub, open := "start", true
	for _, a := range args {
		if a == "--no-open" {
			open = false
		} else {
			sub = a
		}
	}
	ctx, cancel := interrupted()
	defer cancel()
	switch sub {
	case "install":
		fmt.Println("Installing Open WebUI", webui.Version, "onto the drive. This takes a while and uses about 4 GB.")
		if err := webui.Install(ctx, d, netguard.Client(d.Online()), os.Stdout); err != nil {
			return err
		}
		fmt.Println("Done. Start it with: usb-llms webui start")
		return nil
	case "start":
		_, wasRunning := engine.Running(d)
		st, err := engine.Ensure(d)
		if err != nil {
			return err
		}
		if !wasRunning {
			defer func() { _ = engine.Stop(d) }()
		}
		cmd, err := webui.Command(ctx, d, st.URL(), st.Online)
		if err != nil {
			return err
		}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		url := fmt.Sprintf("http://127.0.0.1:%d/", webui.Port)
		fmt.Println("Open WebUI is starting at", url, "(the first start can take minutes from a USB drive). Press Ctrl+C to stop.")
		go func() {
			if waitFor(ctx, url, 10*time.Minute) && open {
				openBrowser(url)
			}
		}()
		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			return fmt.Errorf("Open WebUI stopped: %w", err)
		}
		return nil
	}
	return fmt.Errorf("unknown webui command %q", sub)
}

func waitFor(ctx context.Context, url string, limit time.Duration) bool {
	end := time.Now().Add(limit)
	for time.Now().Before(end) && ctx.Err() == nil {
		res, err := netguard.Local().Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			return true
		}
		time.Sleep(time.Second)
	}
	return false
}

func lvwCmd(d *drive.Drive, args []string) error {
	if len(args) > 0 && args[0] == "install" {
		from := ""
		if len(args) > 1 {
			from = args[1]
		}
		dest, err := lvw.Install(d, from)
		if err != nil {
			return err
		}
		fmt.Println("LvW-harness is on the drive at", dest)
		return nil
	}
	bin, err := lvw.Find(d)
	if err != nil {
		return err
	}
	if _, wasRunning := engine.Running(d); !wasRunning {
		defer func() { _ = engine.Stop(d) }()
	}
	st, err := engine.Ensure(d)
	if err != nil {
		return err
	}
	first := ""
	if list, err := models.List(context.Background(), st.URL()); err == nil && len(list) > 0 {
		first = list[0].Name
	}
	if err := lvw.Sync(d, st.URL(), first); err != nil {
		return err
	}
	c := exec.Command(bin, args...)
	c.Env = lvw.Env(os.Environ(), d, st.URL(), st.Online)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return childExit(c.Run())
}
