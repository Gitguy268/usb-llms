// Package tui is the terminal interface.
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Gitguy268/usb-llms/internal/chat"
	"github.com/Gitguy268/usb-llms/internal/drive"
	"github.com/Gitguy268/usb-llms/internal/engine"
	"github.com/Gitguy268/usb-llms/internal/models"
	"github.com/Gitguy268/usb-llms/internal/netguard"
	"github.com/Gitguy268/usb-llms/internal/sys"
	"github.com/Gitguy268/usb-llms/internal/web"
)

// Action is what the caller should run after the interface closes.
type Action string

// Actions the home screen can hand back.
const (
	ActionNone  Action = ""
	ActionWeb   Action = "web"
	ActionApp   Action = "app"
	ActionWebUI Action = "webui"
	ActionLvw   Action = "lvw"
	// ActionCreate asks what to make in the plain terminal: pictures, speech, 3D and more.
	ActionCreate Action = "create"
)

var (
	title  = lipgloss.NewStyle().Foreground(lipgloss.Color("#d6ae3f")).Bold(true)
	dim    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#55675e", Dark: "#93a89d"})
	active = lipgloss.NewStyle().Foreground(lipgloss.Color("#d6ae3f"))
	errSt  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#a4322a", Dark: "#e8877e"})
	you    = lipgloss.NewStyle().Bold(true)
)

type screen int

const (
	scrHome screen = iota
	scrPick
	scrChat
	scrModels
	scrCatalog
	scrAdd
)

type (
	deltaMsg string
	doneMsg  struct{ err error }
	listMsg  struct {
		list []models.Installed
		err  error
	}
	progressMsg models.Event
	pulledMsg   struct {
		name string
		err  error
	}
	setupDoneMsg struct{ err error }
	opMsg        struct {
		text string
		err  error
	}
)

type model struct {
	d       *drive.Drive
	screen  screen
	cursor  int
	w, h    int
	status  string
	failure string
	action  Action

	list []models.Installed

	input   textinput.Model
	conv    chat.Conversation
	reply   string
	busy    bool
	cancel  context.CancelFunc
	updates chan tea.Msg
	chosen  string
}

// Run shows the interface until the user quits and returns what to launch next.
func Run(d *drive.Drive) (Action, error) {
	in := textinput.New()
	in.Prompt = "> "
	in.CharLimit = 0
	m := model{d: d, input: in, updates: make(chan tea.Msg, 64)}
	out, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return ActionNone, err
	}
	final := out.(model)
	if final.cancel != nil {
		final.cancel()
	}
	return final.action, nil
}

func (m model) Init() tea.Cmd { return tea.Batch(m.waitUpdate(), m.loadList()) }

func (m model) waitUpdate() tea.Cmd {
	return func() tea.Msg { return <-m.updates }
}

func (m model) loadList() tea.Cmd {
	d := m.d
	return func() tea.Msg {
		if _, ok := engine.Installed(d); !ok {
			return listMsg{}
		}
		st, err := engine.Ensure(d)
		if err != nil {
			return listMsg{err: err}
		}
		list, err := models.List(context.Background(), st.URL())
		return listMsg{list: list, err: err}
	}
}

type item struct {
	label string
	run   func(*model) tea.Cmd
}

func (m *model) homeItems() []item {
	var items []item
	if _, ok := engine.Installed(m.d); !ok {
		items = append(items, item{"Set up this computer (downloads the engine)", func(m *model) tea.Cmd { return m.startSetup() }})
	}
	mode := "Switch to online"
	if m.d.Online() {
		mode = "Switch to offline"
	}
	return append(items,
		item{"Chat", func(m *model) tea.Cmd {
			if len(m.list) == 0 {
				m.failure = "There are no models yet. Choose Add a model first."
				return nil
			}
			m.screen, m.cursor = scrPick, 0
			return nil
		}},
		item{"Create a picture, video, speech, music or 3D model", func(m *model) tea.Cmd { m.action = ActionCreate; return tea.Quit }},
		item{"Models on this drive", func(m *model) tea.Cmd { m.screen, m.cursor = scrModels, 0; return m.loadList() }},
		item{"Add a model", func(m *model) tea.Cmd { m.screen, m.cursor = scrCatalog, 0; return nil }},
		item{mode, func(m *model) tea.Cmd { return m.toggleMode() }},
		item{"Open the web interface", func(m *model) tea.Cmd { m.action = ActionWeb; return tea.Quit }},
		item{"Open the app window", func(m *model) tea.Cmd { m.action = ActionApp; return tea.Quit }},
		item{"Open Open WebUI", func(m *model) tea.Cmd { m.action = ActionWebUI; return tea.Quit }},
		item{"Run LvW-harness", func(m *model) tea.Cmd { m.action = ActionLvw; return tea.Quit }},
		item{"Quit", func(m *model) tea.Cmd { return tea.Quit }},
	)
}

func (m *model) toggleMode() tea.Cmd {
	d, online := m.d, !m.d.Online()
	return func() tea.Msg {
		if err := web.SetMode(d, online); err != nil {
			return opMsg{err: err}
		}
		if online {
			return opMsg{text: "Online: downloads and remote services are allowed."}
		}
		return opMsg{text: "Offline: nothing leaves this computer."}
	}
}

func (m *model) startSetup() tea.Cmd {
	d, up := m.d, m.updates
	m.busy, m.status, m.failure = true, "Downloading the engine", ""
	return func() tea.Msg {
		_, err := engine.Install(context.Background(), d, netguard.Client(d.Online()), engine.InstallOptions{
			Progress: func(done, total int64) {
				select {
				case up <- progressMsg(models.Event{Status: "Downloading the engine", Done: done, Total: total}):
				default:
				}
			},
		})
		return setupDoneMsg{err}
	}
}

func (m *model) startPull(spec string) tea.Cmd {
	d, up := m.d, m.updates
	m.busy, m.status, m.failure = true, "Starting", ""
	return func() tea.Msg {
		st, err := engine.Ensure(d)
		if err != nil {
			return pulledMsg{err: err}
		}
		name, err := models.New(d, st.URL()).Pull(context.Background(), spec, "", func(e models.Event) {
			select {
			case up <- progressMsg(e):
			default:
			}
		})
		return pulledMsg{name, err}
	}
}

func (m *model) startChat(content string) tea.Cmd {
	m.conv.Model = m.chosen
	m.conv.Messages = append(m.conv.Messages, chat.Message{Role: "user", Content: content})
	m.reply, m.busy, m.failure = "", true, ""
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	d, up, conv, chosen := m.d, m.updates, m.conv.Messages, m.chosen
	return func() tea.Msg {
		st, err := engine.Ensure(d)
		if err != nil {
			return doneMsg{err}
		}
		err = chat.Stream(ctx, d, st.URL(), chosen, conv, func(delta string) { up <- deltaMsg(delta) })
		return doneMsg{err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.input.Width = max(10, msg.Width-4)
	case listMsg:
		m.list = msg.list
		if msg.err != nil {
			m.failure = msg.err.Error()
		}
		if m.cursor >= len(m.list) && m.screen == scrModels {
			m.cursor = max(0, len(m.list)-1)
		}
	case progressMsg:
		m.status = msg.Status
		if msg.Total > 0 {
			m.status = fmt.Sprintf("%s  %s of %s", msg.Status, sys.GB(uint64(msg.Done)), sys.GB(uint64(msg.Total)))
		}
		return m, m.waitUpdate()
	case deltaMsg:
		m.reply += string(msg)
		return m, m.waitUpdate()
	case doneMsg:
		m.busy = false
		if m.reply != "" {
			m.conv.Messages = append(m.conv.Messages, chat.Message{Role: "assistant", Content: m.reply})
			m.reply = ""
		}
		if err := chat.Save(m.d, &m.conv); err != nil && msg.err == nil {
			msg.err = err
		}
		if msg.err != nil && msg.err != context.Canceled && !strings.Contains(msg.err.Error(), "context canceled") {
			m.failure = msg.err.Error()
		}
	case pulledMsg:
		m.busy = false
		if msg.err != nil {
			m.failure, m.status = msg.err.Error(), ""
			return m, nil
		}
		m.status = "Added " + msg.name
		return m, m.loadList()
	case setupDoneMsg:
		m.busy = false
		if msg.err != nil {
			m.failure, m.status = msg.err.Error(), ""
			return m, nil
		}
		m.status = "The engine is ready."
		return m, m.loadList()
	case opMsg:
		m.status = msg.text
		if msg.err != nil {
			m.failure = msg.err.Error()
		}
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.screen {
	case scrChat:
		return m.keyChat(k)
	case scrAdd:
		switch k.String() {
		case "esc":
			m.screen, m.cursor = scrCatalog, 0
			m.input.Blur()
		case "enter":
			if v := strings.TrimSpace(m.input.Value()); v != "" && !m.busy {
				m.input.SetValue("")
				return m, m.startPull(v)
			}
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(k)
			return m, cmd
		}
		return m, nil
	}
	size := m.listSize()
	switch k.String() {
	case "q":
		if m.screen == scrHome {
			return m, tea.Quit
		}
		m.back()
	case "esc":
		m.back()
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < size-1 {
			m.cursor++
		}
	case "d":
		if m.screen == scrModels && m.cursor < len(m.list) {
			return m, m.deleteSelected()
		}
	case "enter":
		m.failure = ""
		return m.choose()
	}
	return m, nil
}

func (m *model) back() {
	m.screen, m.cursor, m.failure = scrHome, 0, ""
}

func (m *model) deleteSelected() tea.Cmd {
	d, name := m.d, m.list[m.cursor].Name
	return tea.Sequence(func() tea.Msg {
		st, err := engine.Ensure(d)
		if err == nil {
			err = models.Remove(context.Background(), st.URL(), name)
		}
		return opMsg{text: "Deleted " + name, err: err}
	}, m.loadList())
}

func (m *model) listSize() int {
	switch m.screen {
	case scrHome:
		return len(m.homeItems())
	case scrPick, scrModels:
		return len(m.list)
	case scrCatalog:
		return len(models.Catalog()) + 1
	}
	return 0
}

func (m model) choose() (tea.Model, tea.Cmd) {
	switch m.screen {
	case scrHome:
		items := m.homeItems()
		if m.cursor < len(items) {
			cmd := items[m.cursor].run(&m)
			return m, cmd
		}
	case scrPick:
		if m.cursor < len(m.list) {
			m.chosen = m.list[m.cursor].Name
			m.conv = chat.Conversation{}
			m.screen = scrChat
			return m, m.input.Focus()
		}
	case scrCatalog:
		cat := models.Catalog()
		if m.cursor == len(cat) {
			m.screen = scrAdd
			m.status = ""
			return m, m.input.Focus()
		}
		if !m.busy {
			return m, m.startPull(cat[m.cursor].ID)
		}
	}
	return m, nil
}

func (m model) keyChat(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		if m.busy && m.cancel != nil {
			m.cancel()
			return m, nil
		}
		m.input.Blur()
		m.back()
		return m, nil
	case "enter":
		if v := strings.TrimSpace(m.input.Value()); v != "" && !m.busy {
			m.input.SetValue("")
			return m, m.startChat(v)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

func (m model) View() string {
	var b strings.Builder
	mode := "offline"
	if m.d.Online() {
		mode = "online"
	}
	b.WriteString(title.Render("USB LLMS") + dim.Render("  "+mode+"  "+drive.Platform()) + "\n\n")
	switch m.screen {
	case scrHome:
		for i, it := range m.homeItems() {
			b.WriteString(row(i == m.cursor, it.label) + "\n")
		}
		b.WriteString("\n" + dim.Render(fmt.Sprintf("%d models on %s. Arrow keys move, Enter chooses, q quits.", len(m.list), m.d.Root)) + "\n")
	case scrPick:
		b.WriteString("Chat with which model?\n\n")
		for i, x := range m.list {
			b.WriteString(row(i == m.cursor, x.Name) + "\n")
		}
		b.WriteString("\n" + dim.Render("Enter chooses, Esc goes back.") + "\n")
	case scrModels:
		if len(m.list) == 0 {
			b.WriteString("Nothing here yet. Go back and choose Add a model.\n")
		}
		for i, x := range m.list {
			b.WriteString(row(i == m.cursor, fmt.Sprintf("%-40s %s", x.Name, sys.GB(uint64(x.Size)))) + "\n")
		}
		b.WriteString("\n" + dim.Render("d deletes the selected model, Esc goes back.") + "\n")
	case scrCatalog:
		ram := sys.RAMBytes()
		for i, e := range models.Catalog() {
			line := fmt.Sprintf("%-28s %5.2f GB  %-10s", e.Name, e.SizeGB, e.Label)
			if ram > 0 && e.RAMGB*1e9 > float64(ram) {
				line += errSt.Render(fmt.Sprintf(" needs %.0f GB memory", e.RAMGB))
			}
			b.WriteString(row(i == m.cursor, line) + "\n")
		}
		b.WriteString(row(m.cursor == len(models.Catalog()), "Something else: Hugging Face, GitHub, a link or a file") + "\n")
		if m.cursor < len(models.Catalog()) {
			e := models.Catalog()[m.cursor]
			b.WriteString("\n" + dim.Render(e.Note+" Licence: "+e.Licence+".") + "\n")
		}
		b.WriteString("\n" + dim.Render("Enter downloads, Esc goes back.") + "\n")
	case scrAdd:
		b.WriteString("Enter a model: hf:owner/repo:Q4_K_M, gh:owner/repo, a link to a .gguf, or a file path\n\n")
		b.WriteString(m.input.View() + "\n\n" + dim.Render("Enter adds it, Esc goes back.") + "\n")
	case scrChat:
		b.WriteString(m.chatView())
	}
	if m.status != "" && m.screen != scrChat {
		b.WriteString("\n" + active.Render(m.status) + "\n")
	}
	if m.failure != "" {
		b.WriteString("\n" + errSt.Width(max(20, m.w-2)).Render(m.failure) + "\n")
	}
	return b.String()
}

func row(on bool, text string) string {
	if on {
		return active.Render("> ") + text
	}
	return "  " + text
}

// chatView shows the end of the conversation that fits on screen.
func (m model) chatView() string {
	width := max(20, m.w-2)
	wrap := lipgloss.NewStyle().Width(width)
	var parts []string
	for _, msg := range m.conv.Messages {
		if msg.Role == "user" {
			parts = append(parts, you.Render(wrap.Render(msg.Content)))
		} else {
			parts = append(parts, wrap.Render(msg.Content))
		}
	}
	if m.reply != "" {
		parts = append(parts, wrap.Render(m.reply))
	}
	lines := strings.Split(strings.Join(parts, "\n\n"), "\n")
	room := max(3, m.h-8)
	if len(lines) > room {
		lines = lines[len(lines)-room:]
	}
	hint := "Enter sends, Esc goes back."
	if m.busy {
		hint = "Writing a reply. Esc stops it."
	}
	return strings.Join(lines, "\n") + "\n\n" + m.input.View() + "\n" + dim.Render(m.chosen+". "+hint) + "\n"
}
