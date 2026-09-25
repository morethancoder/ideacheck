package tui

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/morethancoder/ideacheck/ideacheck"
	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/store"
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
	// Key says whether an API key is already set, and where: setup offers to
	// keep it rather than asking for it again.
	Key(env string) Key
	HasCLI(name string) bool
	// Search says what research would search with; StartSearch starts the
	// SearXNG `ideacheck search up` runs, reporting each step in Progress.Status.
	Search() SearchState
	StartSearch(ctx context.Context, progress func(Progress)) error
	SaveSetup(p config.Provider, model, effort, key string) error
	// SaveRoles records who judges and who writes; a zero writer means the judge
	// does both. Writer is the current writer, as Current is the judge: all ""
	// when the judge writes too.
	SaveRoles(judge, writer config.Provider) error
	Writer() (backend, model, effort string)
	Engine() (*ideacheck.Engine, error)
	Profile() map[string]string
	SaveProfile(map[string]string) error
	History(limit int) ([]store.Row, error)
	Stored(ref string) (*ideacheck.Result, error)
	Persist(in ideacheck.Intake, res *ideacheck.Result)
}

// SearchState is whether research has something to search with, and whether
// ideacheck could start a search engine here when it has not.
type SearchState struct {
	Enabled   bool   // research.enabled
	With      string // the service a check would query now; "" when there is none
	DockerErr error  // why a search engine cannot be started here; nil when it can
}

// Key is an API key setup found: where it comes from ("your environment",
// "credentials.yaml"; "" = none) and its last characters, enough to recognize
// it by. The key itself never reaches the app.
type Key struct{ From, Tail string }

func (k Key) Found() bool { return k.From != "" }

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
	Intake    ideacheck.Intake // pageLive / pageIdea: the idea to check or prefill
	Options   ideacheck.CheckOptions
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
	{"Settings", "choose the judge and the writer: provider, model and API key", pageSetup},
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
	intake  ideacheck.Intake
	draft   *ideaDraft
	setup   *setupDraft
	dl      *download
	profile map[string]*string
	replies []string
	missing []ideacheck.Missing
	asked   bool
	// answered: fields already offered in the detail steps; never re-asked.
	answered []string
	// earlier: the needs_input result the follow-up questions came from; the
	// re-run keeps its judgments and reconsiders only what was answered.
	earlier *ideacheck.Result

	live    liveModel
	engine  *ideacheck.Engine // the running check's engine; it decides what is worth asking
	done    chan outcome
	cancel  context.CancelFunc
	result  *ideacheck.Result
	tab     int
	history table.Model
	rows    []store.Row
}

// Run opens the app and returns the last result shown (nil when none), so the
// caller can leave it in the terminal's scrollback.
func Run(ctx context.Context, host Host, start Start, out io.Writer) (*ideacheck.Result, error) {
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
		a.note = "Welcome! First, choose the judge — the model that answers every scoring question. A local Ollama model is free and needs no key."
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
		a.intake, a.asked, a.answered, a.earlier = ideacheck.Intake{}, false, nil, nil
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
	title := map[page]string{pageMenu: "Home", pageSetup: "Settings", pageDownload: "Downloading", pageIdea: "New check", pageLive: "Checking", pageAsk: "A few questions", pageResult: "Result", pageHistory: "History", pageProfile: "Profile"}[a.page]
	header := pill.Render("ideacheck") + accent.Render("  ›  ") + bold.Render(title) + "\n" + a.modelLine() + "\n"
	body, keys := a.body()
	if a.note != "" {
		body = callout.BorderForeground(lipgloss.Color("3")).Render(warnSty.Width(max(a.width-9, 20)).Render(a.note)) + "\n\n" + body
	}
	if a.banner != "" {
		body = callout.BorderForeground(lipgloss.Color("1")).Render(bad.Bold(true).Render("✗ ")+bad.Width(max(a.width-11, 20)).Render(a.banner)) + "\n\n" + body
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(header + "\n" + body + "\n\n" + help(max(a.width-4, 20), keys...))
}

// modelLine is who does what, under the title of every page: the saved
// choices, or in Settings the ones being made.
func (a *App) modelLine() string {
	if a.page == pageSetup && a.setup != nil {
		return a.rolesLine(a.setup)
	}
	backend, model, effort := a.host.Current()
	if model == "" && backend == "not set up" {
		return warnSty.Render(backend)
	}
	writer := ""
	if b, m, e := a.host.Writer(); b != "" {
		writer = who(b, m, e)
	}
	return modelLine(formWidth(a.width), who(backend, model, effort), writer, "")
}

// modelLine names the judge, the writer when another model writes, and what
// research searches with when that is worth saying, each written by who.
func modelLine(width int, judge, writer, search string) string {
	parts := []string{dim.Render("judge + writer ") + judge}
	if writer != "" {
		parts = []string{dim.Render("judge ") + judge, dim.Render("writer ") + writer}
	}
	if search != "" {
		parts = append(parts, dim.Render("search ")+search)
	}
	return flow(width, "   ", parts)
}

// who is one model wherever it is named: the backend in the accent and the
// model in bold, so the one thing that decides cost and quality stands out.
func who(backend, model, effort string) string {
	parts := []string{accent.Render(backend)}
	if model != "" {
		parts = append(parts, bold.Render(model))
	}
	if effort != "" {
		parts = append(parts, dim.Render("effort "+effort))
	}
	return strings.Join(parts, dim.Render(" · "))
}

// body is the page and its key bindings, as key/what pairs for help.
func (a *App) body() (string, []string) {
	switch a.page {
	case pageMenu:
		return a.menuView(), []string{"↑/↓", "move", "enter", "select", "q", "quit"}
	case pageSetup, pageAsk: // one field per step: nothing for shift+tab to go back to
		return a.wiz.view(), []string{"enter", "next", "esc", "back", "ctrl+c", "quit"}
	case pageIdea, pageProfile:
		return a.wiz.view(), []string{"enter", "next", "shift+tab", "previous field", "esc", "back", "ctrl+c", "quit"}
	case pageDownload:
		return a.dl.view(a.width), []string{"esc", "stop and go back", "ctrl+c", "quit"}
	case pageLive:
		return a.live.body(), []string{"ctrl+c", "cancel — answers land as they complete"}
	case pageResult:
		return a.resultView(), []string{"←/→", "switch view", "n", "new check", "h", "history", "m", "menu", "q", "quit"}
	case pageHistory:
		return a.historyView(), []string{"↑/↓", "move", "enter", "open", "esc", "back"}
	}
	return "", nil
}

// menuView highlights the line under the cursor as a bar and lets its hint
// read at full strength; the others stay quiet.
func (a *App) menuView() string {
	var b strings.Builder
	label := lipgloss.NewStyle().Width(21) // lines up with the text inside the bar
	for i, item := range menuItems {
		if i == a.cursor {
			b.WriteString(pill.Width(24).Render("› "+item.label) + "  " + item.hint + "\n")
			continue
		}
		b.WriteString("   " + label.Render(item.label) + "  " + dim.Render(item.hint) + "\n")
	}
	return b.String()
}
