package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/pipeline"
	"github.com/morethancoder/ideacheck/internal/store"
)

// Host is everything the app needs from the outside world. The CLI implements it.
type Host interface {
	Providers() []config.Provider
	Current() (backend, model, effort string)
	// Models lists a provider's models for setup, live where it can (key is the
	// one being entered, possibly not saved yet). Each label carries the model's
	// price; with an error, the list is the provider's preset one.
	Models(p config.Provider, key string) ([]config.ModelChoice, error)
	// MissingModel is true when p runs models on this machine and model is not
	// downloaded yet; Download then fetches it, reporting progress as it goes.
	MissingModel(p config.Provider, model string) bool
	Download(ctx context.Context, p config.Provider, model string, progress func(Progress)) error
	HasKey(env string) bool
	HasCLI(name string) bool
	SaveSetup(p config.Provider, model, effort, key string) error
	// SaveRoles records who judges and who writes; a zero writer means the judge
	// does both. Writer names the current writer, "" when there is none.
	SaveRoles(judge, writer config.Provider) error
	Writer() string
	Engine() (*pipeline.Engine, error)
	Profile() map[string]string
	SaveProfile(map[string]string) error
	History(limit int) ([]store.Row, error)
	Stored(ref string) (*pipeline.Result, error)
	Persist(in pipeline.Intake, res *pipeline.Result)
}

type page int

const (
	pageMenu page = iota
	pageSetup
	pageDownload
	pageIdea
	pageLive
	pageAsk
	pageResult
	pageHistory
	pageProfile
)

// Start says where the app opens and whether it exits once that task is done.
type Start struct {
	Page      page
	Intake    pipeline.Intake // pageLive / pageIdea: the idea to check or prefill
	Options   pipeline.Options
	NoAsk     bool // never ask follow-ups (-A)
	ExitAfter bool // quit when the opening task finishes instead of going to the menu
	NeedSetup bool // run setup first, then continue to Page
}

// NotReady is a Host.Engine error the user fixes in Settings (the model cannot
// be reached, or was never downloaded). The app opens setup with Reason shown,
// then carries on with the check.
type NotReady struct{ Reason string }

func (e *NotReady) Error() string { return e.Reason }

// Entry points for the CLI.
var (
	OpenMenu    = pageMenu
	OpenSetup   = pageSetup
	OpenIdea    = pageIdea
	OpenCheck   = pageLive
	OpenHistory = pageHistory
	OpenProfile = pageProfile
)

var menuItems = []struct {
	label, hint string
	page        page
}{
	{"Check an idea", "describe it once, then watch it being researched and judged", pageIdea},
	{"History", "browse and reopen past checks", pageHistory},
	{"Profile", "who you are — used for founder-fit questions", pageProfile},
	{"Settings", "choose the model provider, API key and model", pageSetup},
	{"Quit", "", -1},
}

type App struct {
	host  Host
	start Start
	ctx   context.Context

	page          page
	width, height int
	banner        string // last error, shown until the next action
	note          string // calm guidance (first run, model not reachable), cleared the same way
	cursor        int    // menu

	wiz     *wizard
	intake  pipeline.Intake
	draft   *ideaDraft
	setup   *setupDraft
	dl      *download
	profile map[string]*string
	replies []string
	missing []pipeline.Missing
	asked   bool
	// answered: fields already offered in the detail steps; never re-asked.
	answered []string

	live    liveModel
	engine  *pipeline.Engine // the running check's engine; it decides what is worth asking
	done    chan outcome
	cancel  context.CancelFunc
	result  *pipeline.Result
	tab     int
	history table.Model
	rows    []store.Row
}

// Run opens the app and returns the last result shown (nil when none), so the
// caller can leave it in the terminal's scrollback.
func Run(ctx context.Context, host Host, start Start, out io.Writer) (*pipeline.Result, error) {
	app := &App{host: host, start: start, ctx: ctx, intake: start.Intake, width: 80, height: 24}
	final, err := tea.NewProgram(app, tea.WithContext(ctx), tea.WithOutput(out), tea.WithAltScreen()).Run()
	if err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		return nil, err
	}
	if a, ok := final.(*App); ok {
		return a.result, nil
	}
	return nil, nil
}

func (a *App) Init() tea.Cmd {
	if a.start.NeedSetup {
		cmd := a.open(pageSetup)
		a.note = "Welcome! First, choose how ideacheck reaches a model. A local Ollama model is free and needs no key."
		return cmd
	}
	return a.open(a.start.Page)
}

// open switches page and starts whatever that page needs.
func (a *App) open(p page) tea.Cmd {
	a.page, a.banner, a.note = p, "", ""
	switch p {
	case pageSetup:
		return a.openSetup()
	case pageIdea:
		return a.openIdea()
	case pageProfile:
		return a.openProfile()
	case pageHistory:
		return a.openHistory()
	case pageLive:
		return a.openLive()
	}
	return nil
}

// leave is what happens when the opening task is finished or abandoned.
func (a *App) leave() tea.Cmd {
	if a.start.ExitAfter {
		return tea.Quit
	}
	return a.open(pageMenu)
}

func (a *App) fail(err error) tea.Cmd {
	a.page, a.banner = pageMenu, err.Error()
	return nil
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 && msg.Height > 0 { // some ptys report 0x0: keep the defaults
			a.width, a.height = msg.Width, msg.Height
		}
		a.history.SetHeight(max(a.height-8, 5))
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			if a.cancel != nil {
				a.cancel()
			}
			if a.dl != nil {
				a.dl.cancel()
			}
			return a, tea.Quit
		}
	}
	switch a.page {
	case pageMenu:
		return a, a.updateMenu(msg)
	case pageSetup, pageIdea, pageAsk, pageProfile:
		return a, a.updateWizard(msg)
	case pageDownload:
		return a, a.updateDownload(msg)
	case pageLive:
		return a, a.updateLive(msg)
	case pageResult:
		return a, a.updateResult(msg)
	case pageHistory:
		return a, a.updateHistory(msg)
	}
	return a, nil
}

func (a *App) updateMenu(msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch key.String() {
	case "up", "k":
		a.cursor = (a.cursor + len(menuItems) - 1) % len(menuItems)
	case "down", "j":
		a.cursor = (a.cursor + 1) % len(menuItems)
	case "q", "esc":
		return tea.Quit
	case "enter":
		if menuItems[a.cursor].page < 0 {
			return tea.Quit
		}
		a.intake, a.asked, a.answered = pipeline.Intake{}, false, nil
		return a.open(menuItems[a.cursor].page)
	}
	return nil
}

// updateWizard drives whichever wizard is open and routes its outcome.
func (a *App) updateWizard(msg tea.Msg) tea.Cmd {
	state, cmd := a.wiz.update(msg)
	switch state {
	case wizardLeft:
		return a.leave()
	case wizardDone:
		return a.wizardDone()
	}
	return cmd
}

func (a *App) wizardDone() tea.Cmd {
	switch a.page {
	case pageSetup:
		return a.finishSetup()
	case pageIdea:
		a.intake, a.answered = a.draft.intake(a.intake), a.draft.offered()
		return a.open(pageLive)
	case pageAsk:
		a.intake = ApplyReplies(a.intake, a.missing, a.replies)
		a.asked = true
		return a.open(pageLive)
	case pageProfile:
		profile := map[string]string{}
		for name, v := range a.profile {
			if s := strings.TrimSpace(*v); s != "" {
				profile[name] = s
			}
		}
		if err := a.host.SaveProfile(profile); err != nil {
			return a.fail(err)
		}
		return a.leave()
	}
	return nil
}

func (a *App) View() string {
	backend, model, effort := a.host.Current()
	if effort != "" {
		effort = "effort " + effort
	}
	title := map[page]string{pageMenu: "Home", pageSetup: "Settings", pageDownload: "Downloading", pageIdea: "New check", pageLive: "Checking", pageAsk: "A few questions", pageResult: "Result", pageHistory: "History", pageProfile: "Profile"}[a.page]
	writer := a.host.Writer()
	if writer != "" {
		writer = "written by " + writer
	}
	header := bold.Render("ideacheck") + dim.Render("  ›  "+title) + "\n" + dim.Render(joinNonEmpty(" · ", backend, model, effort, writer)) + "\n"
	body, help := a.body()
	if a.note != "" {
		body = warnSty.Width(max(a.width-6, 20)).Render(a.note) + "\n\n" + body
	}
	if a.banner != "" {
		body = bad.Render("! "+a.banner) + "\n\n" + body
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(header + "\n" + body + "\n\n" + dim.Render(help))
}

func (a *App) body() (string, string) {
	switch a.page {
	case pageMenu:
		return a.menuView(), "↑/↓ move · enter select · q quit"
	case pageSetup, pageIdea, pageAsk, pageProfile:
		return a.wiz.view(), "enter next · shift+tab previous field · esc back · ctrl+c quit"
	case pageDownload:
		return a.dl.view(a.width), "esc stop and go back · ctrl+c quit"
	case pageLive:
		return a.live.body(), "answers land as they complete · ctrl+c cancel"
	case pageResult:
		return a.resultView(), "←/→ or tab switch view · n new check · h history · m menu · q quit"
	case pageHistory:
		return a.historyView(), "↑/↓ move · enter open · esc back"
	}
	return "", ""
}

func (a *App) menuView() string {
	var b strings.Builder
	for i, item := range menuItems {
		line := "  " + item.label
		if i == a.cursor {
			line = bold.Render("› " + item.label)
		}
		b.WriteString(fmt.Sprintf("%-28s %s\n", line, dim.Render(item.hint)))
	}
	return b.String()
}

func joinNonEmpty(sep string, parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}
