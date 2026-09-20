package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/judge/backends"
	"github.com/morethancoder/ideacheck/internal/pipeline"
)

// ---- Settings ---------------------------------------------------------------

// otherModel is the "type any model id" choice in the model list.
const otherModel = "\x00other"

type setupDraft struct {
	id, key       string
	model, custom string // model is the list choice; custom the typed id (otherModel, or no list)
	effort        string
	providers     []config.Provider
	current       [3]string // backend, model, effort in effect when setup opened
	defaultsFor   string    // provider id the model/effort defaults were set for
	lists         map[string][]config.ModelChoice
	note          string // why the list is the preset one (discovery failed)
}

func (d *setupDraft) chosen() config.Provider { return providerByID(d.providers, d.id) }

// models is the list for the chosen provider, once loaded.
func (d *setupDraft) models() []config.ModelChoice { return d.lists[d.id] }

func (d *setupDraft) modelID() string {
	if len(d.models()) == 0 || d.model == otherModel {
		return strings.TrimSpace(d.custom)
	}
	return d.model
}

// efforts offered for the chosen model: its own list, none, or the provider's.
func (d *setupDraft) efforts() []string {
	for _, m := range d.models() {
		if m.ID == d.model && d.model != otherModel {
			if m.NoEffort {
				return nil
			}
			if len(m.Efforts) > 0 {
				return m.Efforts
			}
		}
	}
	return d.chosen().Efforts
}

// loadModels fetches the chosen provider's list once and sets the model and
// effort defaults: what is configured now when it is the same provider, else
// the provider's default model at low effort.
func (a *App) loadModels(d *setupDraft) {
	p := d.chosen()
	discovered := false
	if _, ok := d.lists[d.id]; !ok {
		ms, err := a.host.Models(p, strings.TrimSpace(d.key))
		d.note = ""
		switch {
		case err != nil:
			d.note = "Could not list models (" + err.Error() + "); showing the usual ones."
		case len(ms) == 0 && p.Discover == "ollama":
			d.note = "No models are downloaded yet. Type the one to use (any name from ollama.com/library) and ideacheck downloads it."
		case p.Discover != "":
			discovered = len(ms) > 0
		}
		if len(ms) == 0 { // the host sends the priced preset list with a discovery error
			ms = p.Models
		}
		if p.Model == "" {
			ms = append([]config.ModelChoice{{ID: "", Label: "Default — the provider's own choice"}}, ms...)
		}
		d.lists[d.id] = ms
	}
	if d.defaultsFor == d.id {
		return
	}
	d.defaultsFor = d.id
	want, effort := p.Model, "low"
	if d.current[0] == p.Backend {
		want, effort = d.current[1], d.current[2]
	}
	d.model, d.custom, d.effort = otherModel, want, effort
	for _, m := range d.models() {
		if m.ID == want {
			d.model, d.custom = want, ""
		}
	}
	// A discovered list is what is actually installed: never default to a model
	// that is not in it (enter-enter-enter would save one that cannot run).
	if discovered && d.model == otherModel {
		d.model, d.custom = d.models()[0].ID, ""
	}
	if !slices.Contains(d.efforts(), d.effort) {
		d.effort = ""
	}
}

func (a *App) openSetup() tea.Cmd {
	d := &setupDraft{providers: a.host.Providers(), lists: map[string][]config.ModelChoice{}}
	backend, model, effort := a.host.Current()
	d.current = [3]string{backend, model, effort}
	var options []huh.Option[string]
	for _, p := range d.providers {
		label := p.Label
		if p.NeedsCLI != "" && !a.host.HasCLI(p.NeedsCLI) {
			label += "  (" + p.NeedsCLI + " not installed)"
		}
		options = append(options, huh.NewOption(label, p.ID))
		if d.id == "" && p.Backend == backend {
			d.id = p.ID
		}
	}
	a.setup = d
	steps := []step{
		{title: "Provider", build: func() *huh.Form {
			return huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("How should ideacheck reach a model?").
				Description("You can change this any time from Settings or `ideacheck setup`.").Options(options...).Value(&d.id).
				Validate(func(id string) error {
					if cli := providerByID(d.providers, id).NeedsCLI; cli != "" && !a.host.HasCLI(cli) {
						return fmt.Errorf("the %s command is not installed; pick another provider", cli)
					}
					return nil
				})))
		}},
		{title: "API key", skip: func() bool { return d.chosen().KeyEnv == "" }, build: func() *huh.Form {
			p := d.chosen()
			desc := "Get one at " + p.KeyURL + "\nStored in credentials.yaml (readable only by you). $" + p.KeyEnv + " in your environment wins if set."
			if a.host.HasKey(p.KeyEnv) {
				desc = "A key is already available. Leave blank to keep it.\n" + desc
			}
			return huh.NewForm(huh.NewGroup(huh.NewInput().Title(p.KeyEnv).Description(desc).EchoMode(huh.EchoModePassword).Value(&d.key).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" && !a.host.HasKey(p.KeyEnv) {
						return errors.New("an API key is required for this provider")
					}
					return nil
				})))
		}},
		{title: "Model", build: func() *huh.Form {
			a.loadModels(d)
			p := d.chosen()
			if len(d.models()) == 0 {
				// Blank is a real answer only for a provider that has its own default.
				desc := "The model id this provider should use.\n" + customHint(p)
				if p.Model == "" {
					desc = "The model id this provider should use. Leave blank for the provider's own default.\n" + d.note
				}
				return huh.NewForm(huh.NewGroup(customModel(d, "Model", strings.TrimSpace(desc), p.Model != "")))
			}
			var opts []huh.Option[string]
			for _, m := range d.models() {
				opts = append(opts, huh.NewOption(m.Label, m.ID))
			}
			other := "Other… (type a model id)"
			if d.chosen().Billing == "local" {
				other = "Other… (type any model id — downloaded for you if it is not here yet)"
			}
			opts = append(opts, huh.NewOption(other, otherModel))
			return huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Which model should judge your ideas?").
				Description(strings.TrimSpace(priceNote(d.chosen().Billing) + " " + d.note)).Options(opts...).Value(&d.model)))
		}},
		{title: "Model id", skip: func() bool { return len(d.models()) == 0 || d.model != otherModel }, build: func() *huh.Form {
			return huh.NewForm(huh.NewGroup(customModel(d, "Model id", customHint(d.chosen()), true)))
		}},
		{title: "Effort", skip: func() bool { return len(d.efforts()) == 0 }, build: func() *huh.Form {
			// The CLI backends read "no effort" as thinking off; an API model keeps its own default.
			cli := backends.HumanOnly(d.chosen().Backend)
			opts := []huh.Option[string]{huh.NewOption("Default — the model decides", "")}
			if cli {
				opts[0] = huh.NewOption("Off — no thinking. Recommended: fastest, and each question is one narrow judgment", "")
			}
			for _, e := range d.efforts() {
				label := e
				if e == "low" && !cli {
					label += " — recommended: each question is one short, narrow judgment"
				}
				opts = append(opts, huh.NewOption(label, e))
			}
			return huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("How hard should the model think?").
				Description("Higher effort spends more tokens (and time) per question.").Options(opts...).Value(&d.effort)))
		}},
	}
	var cmd tea.Cmd
	a.wiz, cmd = newWizard("setup", a.width, a.height, steps)
	return cmd
}

// priceNote explains the price on each model: what it costs through this
// provider, or for a subscription login, the API rate to compare against.
func priceNote(billing string) string {
	switch billing {
	case "local":
		return "Local models cost nothing to run."
	case "subscription":
		return "Your plan covers these; prices are the API rate per million tokens in / out, to compare."
	}
	return "Prices are per million tokens in / out."
}

func customHint(p config.Provider) string {
	if p.Billing == "local" {
		return "Any model from ollama.com/library, e.g. qwen3:8b. If it is not on this machine yet, it is downloaded when you finish."
	}
	return "Any model id this provider accepts."
}

// customModel is the "type a model id" field. required rejects a blank id:
// choosing "Other…" and pressing enter must not save a model that is no model.
func customModel(d *setupDraft, title, desc string, required bool) huh.Field {
	return huh.NewInput().Title(title).Description(desc).Value(&d.custom).Validate(func(s string) error {
		if required && strings.TrimSpace(s) == "" {
			return errors.New("type a model id, or press esc to pick one from the list")
		}
		return nil
	})
}

func providerByID(ps []config.Provider, id string) config.Provider {
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	return config.Provider{}
}

// finishSetup downloads the chosen model first when this machine runs it and
// does not have it yet, then saves.
func (a *App) finishSetup() tea.Cmd {
	if p, model := a.setup.chosen(), a.setup.modelID(); a.host.MissingModel(p, model) {
		return a.openDownload(p, model)
	}
	return a.saveSetup()
}

func (a *App) saveSetup() tea.Cmd {
	d := a.setup
	effort := d.effort
	if len(d.efforts()) == 0 {
		effort = ""
	}
	if err := a.host.SaveSetup(d.chosen(), d.modelID(), effort, strings.TrimSpace(d.key)); err != nil {
		return a.fail(err)
	}
	if a.start.NeedSetup { // first run: carry on to what the user came for
		a.start.NeedSetup = false
		return a.open(a.start.Page)
	}
	return a.leave()
}

// ---- New check: the idea, step by step ---------------------------------------

type ideaDraft struct {
	idea    string
	details bool
	fields  map[string]*string
}

// detailSteps are the idea fields offered page by page, in order.
var detailSteps = []struct {
	title  string
	fields []string
}{
	{"Problem and audience", []string{"problem", "audience"}},
	{"Solution and timing", []string{"solution", "why_now"}},
	{"Market", []string{"competitors_known", "differentiation", "monetization"}},
}

// offered is every field the user was shown in the detail steps: a blank one
// is an answer ("I don't know"), so the follow-ups must not ask for it again.
func (d *ideaDraft) offered() []string {
	if !d.details {
		return nil
	}
	var out []string
	for _, s := range detailSteps {
		out = append(out, s.fields...)
	}
	return out
}

func (d *ideaDraft) intake(base pipeline.Intake) pipeline.Intake {
	out := pipeline.Intake{Idea: strings.TrimSpace(d.idea), Fields: map[string]string{}, Profile: base.Profile}
	for name, v := range d.fields {
		if s := strings.TrimSpace(*v); s != "" {
			out.Fields[name] = s
		}
	}
	return out
}

func (a *App) openIdea() tea.Cmd {
	d := &ideaDraft{idea: a.intake.Idea, details: true, fields: map[string]*string{}}
	for _, name := range pipeline.IdeaFields() {
		v := a.intake.Fields[name]
		d.fields[name] = &v
	}
	if len(a.intake.Profile) == 0 {
		a.intake.Profile = a.host.Profile()
	}
	a.draft = d
	inputs := func(names ...string) func() *huh.Form {
		return func() *huh.Form {
			var fields []huh.Field
			for _, n := range names {
				fields = append(fields, huh.NewText().Title(fieldTitles[n]).Description(fieldHints[n]).Lines(2).Value(d.fields[n]))
			}
			return huh.NewForm(huh.NewGroup(fields...))
		}
	}
	noDetails := func() bool { return !d.details }
	steps := []step{
		{title: "The idea", build: func() *huh.Form {
			return huh.NewForm(huh.NewGroup(
				huh.NewText().Title("What is the idea?").Description("A sentence or a page. Plain description beats a pitch.").Lines(5).Value(&d.idea).
					Validate(func(s string) error {
						if strings.TrimSpace(s) == "" {
							return errors.New("describe the idea")
						}
						return nil
					}),
				huh.NewConfirm().Title("Add details step by step?").Description("Recommended: labelled details are judged more reliably than prose. Every one is optional.").
					Affirmative("Yes").Negative("Skip, just check it").Value(&d.details)))
		}},
	}
	for _, ds := range detailSteps {
		steps = append(steps, step{title: ds.title, skip: noDetails, build: inputs(ds.fields...)})
	}
	var cmd tea.Cmd
	a.wiz, cmd = newWizard("idea", a.width, a.height, steps)
	return cmd
}

// ---- Profile ---------------------------------------------------------------

func (a *App) openProfile() tea.Cmd {
	current := a.host.Profile()
	a.profile = map[string]*string{}
	for _, name := range pipeline.ProfileFields() {
		v := current[name]
		a.profile[name] = &v
	}
	inputs := func(names ...string) func() *huh.Form {
		return func() *huh.Form {
			var fields []huh.Field
			for _, n := range names {
				fields = append(fields, huh.NewInput().Title(fieldTitles[n]).Value(a.profile[n]))
			}
			return huh.NewForm(huh.NewGroup(fields...))
		}
	}
	var cmd tea.Cmd
	a.wiz, cmd = newWizard("profile", a.width, a.height, []step{
		{title: "What you bring", build: inputs("skills", "domains", "background")},
		{title: "Reach and commitment", build: inputs("network", "would_use_myself", "time_horizon")},
	})
	return cmd
}

// ---- Follow-up questions ---------------------------------------------------

func (a *App) openAsk(missing []pipeline.Missing) tea.Cmd {
	a.page, a.missing, a.replies = pageAsk, missing, make([]string, len(missing))
	steps := make([]step, len(missing))
	for i, m := range missing {
		steps[i] = step{title: "Missing information", build: func() *huh.Form {
			return huh.NewForm(huh.NewGroup(huh.NewText().Title(m.Ask).Description("Leave blank if you do not know — not knowing is an answer.").Lines(3).Value(&a.replies[i])))
		}}
	}
	var cmd tea.Cmd
	a.wiz, cmd = newWizard("ask", a.width, a.height, steps)
	return cmd
}

// ---- Live check -------------------------------------------------------------

func (a *App) openLive() tea.Cmd {
	engine, err := a.host.Engine()
	var notReady *NotReady
	if errors.As(err, &notReady) { // fixable in Settings: go there, then back to this check
		a.start.NeedSetup = true
		cmd := a.open(pageSetup)
		a.note = notReady.Reason
		return cmd
	}
	if err != nil {
		return a.fail(err)
	}
	if len(a.intake.Profile) == 0 {
		a.intake.Profile = a.host.Profile()
	}
	ctx, cancel := context.WithCancel(a.ctx)
	events := make(chan pipeline.Event, 64)
	a.done, a.cancel = make(chan outcome, 1), cancel
	opts := a.start.Options
	opts.Events, opts.Proceed = events, opts.Proceed || a.asked // asked once: judge with what is known
	opts.Answered = append(opts.Answered, a.answered...)
	in := a.intake
	go func() {
		res, err := engine.Check(ctx, in, opts)
		close(events)
		a.done <- outcome{res, err}
	}()
	a.live = newLive("", events)
	return a.live.Init()
}

func (a *App) updateLive(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case eventMsg:
		a.live = a.live.apply(pipeline.Event(msg))
		return a.live.wait()
	case spinner.TickMsg:
		var cmd tea.Cmd
		a.live.spin, cmd = a.live.spin.Update(msg)
		return cmd
	case closedMsg:
		got := <-a.done
		a.cancel()
		a.cancel = nil
		if got.err != nil {
			return a.fail(got.err)
		}
		if got.res.Status == pipeline.StatusNeedsInput && !a.asked && !a.start.NoAsk {
			return a.openAsk(pipeline.Unanswered(got.res.Missing, a.intake, a.answered))
		}
		a.host.Persist(a.intake, got.res)
		a.result, a.tab, a.page = got.res, 0, pageResult
	}
	return nil
}

// ---- Result ----------------------------------------------------------------

var resultTabs = []string{"Overview", "All scores", "Gaps", "Details"}

func (a *App) updateResult(msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch key.String() {
	case "right", "tab", "l":
		a.tab = (a.tab + 1) % len(resultTabs)
	case "left", "shift+tab":
		a.tab = (a.tab + len(resultTabs) - 1) % len(resultTabs)
	case "n":
		a.intake, a.asked, a.answered = pipeline.Intake{}, false, nil
		return a.open(pageIdea)
	case "h":
		return a.open(pageHistory)
	case "m", "esc":
		return a.leave()
	case "q":
		return tea.Quit
	}
	return nil
}

func (a *App) resultView() string {
	var tabs []string
	for i, name := range resultTabs {
		if i == a.tab {
			tabs = append(tabs, bold.Underline(true).Render(name))
		} else {
			tabs = append(tabs, dim.Render(name))
		}
	}
	res := a.result
	body := ""
	switch a.tab {
	case 0:
		body = headline(res) + "\n"
		if res.Summary != "" {
			body += "\n" + summaryView(res.Summary, a.width-6) + "\n"
		}
		body += contributions("Top strengths", good.Render("▲"), res.TopStrengths) + contributions("Top risks", bad.Render("▼"), res.TopRisks)
		if line := costLine(res.Cost); line != "" {
			body += "\n" + line
		}
	case 1:
		body = ScoresView(res)
	case 2:
		body = gapsView(res)
	case 3:
		body = detailsView(res)
	}
	return strings.Join(tabs, "   ") + "\n\n" + body
}

// ScoresView lists every rubric dimension: the scores are the explanation.
func ScoresView(res *pipeline.Result) string {
	if len(res.Dimensions) == 0 {
		return dim.Render("No rubric was scored for this check.")
	}
	var b strings.Builder
	for _, d := range res.Dimensions {
		switch {
		case d.Error != "":
			fmt.Fprintf(&b, "%s %-28s %s\n", bad.Render("✗"), d.ID, bad.Render(d.Error))
		case d.Value == nil:
			fmt.Fprintf(&b, "  %-28s %s\n", d.ID, dim.Render("not valued"))
		default:
			fmt.Fprintf(&b, "%s %-28s %s %.2f  %s  %s\n", Arrow(d.Polarity), d.ID, Bar(*d.Value), *d.Value, dim.Render(pct(d.Confidence)), dim.Render(fmt.Sprintf("weight %.1f", d.Weight)))
		}
	}
	return b.String() + dim.Render("\n↑ higher is better · ↓ higher is worse · · informational (weight 0)")
}

func gapsView(res *pipeline.Result) string {
	if len(res.Missing) == 0 {
		return good.Render("✓") + " Nothing important is missing from the description."
	}
	return missingPanel(res.Missing)
}

func detailsView(res *pipeline.Result) string {
	var b strings.Builder
	for _, w := range res.Warnings {
		b.WriteString(warnSty.Render("! "+w) + "\n")
	}
	return b.String() + costDetails(res.Cost) + footer(res)
}

// ---- History ----------------------------------------------------------------

func (a *App) openHistory() tea.Cmd {
	rows, err := a.host.History(200)
	if err != nil {
		return a.fail(err)
	}
	a.rows = rows
	trows := make([]table.Row, len(rows))
	for i, r := range rows {
		verdict := r.Verdict
		if verdict == "" {
			verdict = r.Status
		}
		trows[i] = table.Row{fmt.Sprint(r.Seq), strings.Replace(strings.TrimSuffix(r.CreatedAt, "Z"), "T", " ", 1), verdict, fmt.Sprintf("%.2f", r.Composite), r.Rubric, r.Idea}
	}
	a.history = table.New(table.WithFocused(true), table.WithHeight(max(a.height-8, 5)), table.WithRows(trows), table.WithColumns([]table.Column{
		{Title: "#", Width: 4}, {Title: "When", Width: 19}, {Title: "Verdict", Width: 11}, {Title: "Score", Width: 5}, {Title: "Rubric", Width: 12}, {Title: "Idea", Width: max(a.width-70, 20)},
	}))
	return nil
}

func (a *App) updateHistory(msg tea.Msg) tea.Cmd {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc", "q", "m":
			return a.leave()
		case "enter":
			if i := a.history.Cursor(); i >= 0 && i < len(a.rows) {
				res, err := a.host.Stored(a.rows[i].ID)
				if err != nil {
					return a.fail(err)
				}
				a.result, a.tab, a.page = res, 0, pageResult
			}
			return nil
		}
	}
	var cmd tea.Cmd
	a.history, cmd = a.history.Update(msg)
	return cmd
}

func (a *App) historyView() string {
	if len(a.rows) == 0 {
		return dim.Render("No checks yet.")
	}
	return a.history.View()
}
