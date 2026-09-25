package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/morethancoder/ideacheck/ideacheck"
	"github.com/morethancoder/ideacheck/internal/backends"
	"github.com/morethancoder/ideacheck/internal/config"
)

// ---- Settings ---------------------------------------------------------------

// otherModel is the "type any model id" choice in the model list.
const otherModel = "\x00other"

type setupDraft struct {
	id, key     string // the judge's provider, and a key typed for it
	replaceKey  bool   // a key is already set and the user chose to replace it
	writer      string // the writer's provider id; sameWriter = the judge writes too, noWriter = nobody
	providers   []config.Provider
	current     [2][3]string     // backend, model, effort of the judge and the writer when setup opened
	picks       map[string]*pick // by provider id, once that provider's models were listed
	fetched     map[string]bool  // models downloaded during this setup
	search      *SearchState     // asked once: the wizard counts its steps on every draw
	startSearch bool             // start the search engine once the choices are saved
}

// pick is the model and effort chosen for one provider. Every provider keeps
// its own, so the judge and the writer are each picked the same way, and going
// back to change a provider never carries a model across.
type pick struct {
	models        []config.ModelChoice
	note          string // why the list is the preset one (discovery failed)
	model, custom string // model is the list choice; custom the typed id (otherModel, or no list)
	effort        string
}

// searching is what research has to search with, asked of the host once.
func (a *App) searching(d *setupDraft) SearchState {
	if d.search == nil {
		s := a.host.Search()
		d.search = &s
	}
	return *d.search
}

func (d *setupDraft) chosen() config.Provider { return providerByID(d.providers, d.id) }

// The two answers in the writer list that name no other provider: the judge
// writes as well (a chat model), or nobody does (beside a judge that cannot).
const (
	sameWriter = "\x00same"
	noWriter   = "\x00none"
)

// writerChoice is the other provider picked in the Writer step; zero when the
// judge writes too, or nobody does.
func (d *setupDraft) writerChoice() config.Provider {
	if d.writer == noWriter || d.writer == sameWriter || d.writer == d.id {
		return config.Provider{}
	}
	return providerByID(d.providers, d.writer)
}

// roles is who judges and who writes, from the choices made.
func (d *setupDraft) roles() (judge, writer config.Provider) {
	return d.chosen(), d.writerChoice()
}

// key is the API key already set for the chosen provider, if any.
func (a *App) key(d *setupDraft) Key {
	if env := d.chosen().KeyEnv; env != "" {
		return a.host.Key(env)
	}
	return Key{}
}

// usable reports whether a provider can run right now without more setup.
func (a *App) usable(p config.Provider) bool {
	return (p.NeedsCLI == "" || a.host.HasCLI(p.NeedsCLI)) && (p.KeyEnv == "" || a.host.Key(p.KeyEnv).Found())
}

// writers are the choices for who writes beside the chosen judge: the judge
// itself when it can, every other provider that can write and is ready to use,
// and nobody when the judge only judges.
func (a *App) writers(d *setupDraft) []huh.Option[string] {
	chosen := d.chosen()
	var opts []huh.Option[string]
	if !chosen.NeedsWriter {
		opts = append(opts, huh.NewOption("The same model — "+chosen.ID+" judges and writes", sameWriter))
	}
	for _, p := range d.providers {
		if !p.NeedsWriter && p.ID != chosen.ID && a.usable(p) {
			opts = append(opts, huh.NewOption(p.Label, p.ID))
		}
	}
	if chosen.NeedsWriter {
		opts = append(opts, huh.NewOption("Nobody — scores only: no web research, no reading of long text, no summary", noWriter))
	}
	return opts
}

// pickOf is what was chosen for p; before its models were listed, nothing.
func (d *setupDraft) pickOf(p config.Provider) *pick {
	if k, ok := d.picks[p.ID]; ok {
		return k
	}
	return &pick{}
}

// was is the model and effort p's backend had when setup opened.
func (d *setupDraft) was(p config.Provider) (model, effort string, ok bool) {
	for _, cur := range d.current {
		if cur[0] != "" && cur[0] == p.Backend {
			return cur[1], cur[2], true
		}
	}
	return "", "", false
}

// modelOf is the model p would run: the one picked, else the one it runs now,
// else its preset — what saving would leave in config.yaml.
func (d *setupDraft) modelOf(p config.Provider) string {
	if k, ok := d.picks[p.ID]; ok {
		return k.modelID()
	}
	if model, _, ok := d.was(p); ok {
		return model
	}
	return p.Model
}

func (k *pick) modelID() string {
	if len(k.models) == 0 || k.model == otherModel {
		return strings.TrimSpace(k.custom)
	}
	return k.model
}

// efforts offered for the picked model: its own list, none, or the provider's.
func (k *pick) efforts(p config.Provider) []string {
	for _, m := range k.models {
		if m.ID == k.model && k.model != otherModel {
			if m.NoEffort {
				return nil
			}
			if len(m.Efforts) > 0 {
				return m.Efforts
			}
		}
	}
	return p.Efforts
}

// saved is the effort to save: none for a model that takes none.
func (k *pick) saved(p config.Provider) string {
	if len(k.efforts(p)) == 0 {
		return ""
	}
	return k.effort
}

// loadModels fetches p's list once and sets the model and effort defaults:
// what is configured now when it is the same backend, else the provider's
// default model — at low effort for a judge, whose questions are narrow, and
// at the model's own for a writer.
func (a *App) loadModels(d *setupDraft, p config.Provider, role string) *pick {
	if k, ok := d.picks[p.ID]; ok {
		return k
	}
	k := &pick{}
	d.picks[p.ID] = k
	key := ""
	if p.ID == d.id {
		key = strings.TrimSpace(d.key)
	}
	ms, err := a.host.Models(p, key)
	discovered := false
	switch {
	case err != nil:
		k.note = "Could not list models (" + err.Error() + "); showing the usual ones."
	case len(ms) == 0 && p.Discover == "ollama":
		k.note = "No models are downloaded yet. Type the one to use (any name from ollama.com/library) and ideacheck downloads it."
	case p.Discover != "":
		discovered = len(ms) > 0
	}
	if len(ms) == 0 { // the host sends the priced preset list with a discovery error
		ms = p.Models
	}
	if p.Model == "" {
		ms = append([]config.ModelChoice{{ID: "", Label: "Default — the provider's own choice"}}, ms...)
	}
	k.models = ms

	want, effort := p.Model, "low"
	if role == roleWriter {
		effort = ""
	}
	model, e, saved := d.was(p)
	if saved {
		want, effort = model, e
	}
	k.model, k.custom, k.effort = otherModel, want, effort
	for _, m := range k.models {
		if m.ID == want {
			k.model, k.custom = want, ""
		}
	}
	// A discovered list is what the provider serves today: never default to a
	// preset it no longer lists (enter-enter-enter would save one that cannot
	// run). A saved model this provider's presets name stays, typed under
	// "Other…": a pinned version may still answer after the list stopped
	// naming it. (was matches by backend, so a saved id from another provider
	// on the same backend is not kept.)
	pinned := saved && slices.ContainsFunc(p.Models, func(m config.ModelChoice) bool { return m.ID == want })
	if discovered && k.model == otherModel && !pinned {
		k.model, k.custom = k.models[0].ID, ""
	}
	if len(k.models) > 15 {
		k.note = strings.TrimSpace(k.note + " Type / to filter.")
	}
	if !slices.Contains(k.efforts(p), k.effort) {
		k.effort = ""
	}
	return k
}

func (a *App) openSetup() tea.Cmd {
	d := &setupDraft{providers: a.host.Providers(), picks: map[string]*pick{}, fetched: map[string]bool{}}
	d.current[0][0], d.current[0][1], d.current[0][2] = a.host.Current()
	d.current[1][0], d.current[1][1], d.current[1][2] = a.host.Writer()
	var options []huh.Option[string]
	for _, p := range d.providers {
		label := p.Label
		if p.NeedsCLI != "" && !a.host.HasCLI(p.NeedsCLI) {
			label += "  (" + p.NeedsCLI + " not installed)"
		}
		options = append(options, huh.NewOption(label, p.ID))
		if d.id == "" && p.Backend == d.current[0][0] {
			d.id = p.ID
		}
		// Settings opens on what is configured, the writer included; with none
		// configured the Writer step offers its first choice.
		if d.writer == "" && p.Backend == d.current[1][0] && !p.NeedsWriter && a.usable(p) {
			d.writer = p.ID
		}
	}
	a.setup = d
	steps := []step{
		{title: "Judge", build: func() *huh.Form {
			return huh.NewForm(huh.NewGroup(choose("Choose the judge",
				roleNotes[roleJudge]+"\nPick where it runs; you pick the exact model next. A provider marked “judges only” needs a writer beside it, which you choose after; a chat model can do both. Change any of this later in Settings or `ideacheck setup`.", options, &d.id).
				Validate(func(id string) error {
					if cli := providerByID(d.providers, id).NeedsCLI; cli != "" && !a.host.HasCLI(cli) {
						return fmt.Errorf("the %s command is not installed; pick another provider", cli)
					}
					return nil
				})))
		}},
		{title: "API key", skip: func() bool { return !a.key(d).Found() }, build: func() *huh.Form {
			p, k := d.chosen(), a.key(d)
			keep := "Keep it — $" + p.KeyEnv + " from " + k.From
			if k.Tail != "" {
				keep += ", ending in …" + k.Tail
			}
			return huh.NewForm(huh.NewGroup(choose("An API key for "+p.Label+" is already set",
				"Keep the key you have, or replace it with another one.",
				[]huh.Option[bool]{huh.NewOption(keep, false), huh.NewOption("Replace it with a new key", true)}, &d.replaceKey)))
		}},
		{title: "API key", skip: func() bool {
			return d.chosen().KeyEnv == "" || (a.key(d).Found() && !d.replaceKey)
		}, build: func() *huh.Form {
			p := d.chosen()
			desc := "Get one at " + p.KeyURL + "\nStored in credentials.yaml (readable only by you)."
			if a.key(d).From == "your environment" {
				// Saving would change nothing on the next run: say so before the user types.
				desc += "\n$" + p.KeyEnv + " is also set in your environment (your shell, or a .env it loads) and that one wins: remove it there for this key to take effect."
			} else {
				desc += " $" + p.KeyEnv + " in your environment wins if set."
			}
			return huh.NewForm(huh.NewGroup(huh.NewInput().Title(p.KeyEnv).Description(desc).EchoMode(huh.EchoModePassword).Value(&d.key).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("paste the API key, or press esc to go back")
					}
					return nil
				})))
		}},
	}
	steps = append(steps, a.modelSteps(d, [3]string{"Judge model", "Judge model id", "Judge effort"}, d.chosen, d.role, nil)...)
	// The writer is asked whenever there is a choice to make: another provider
	// ready to write, or nobody, beside a judge that only judges.
	steps = append(steps,
		step{title: "Writer", skip: func() bool { return len(a.writers(d)) < 2 }, build: func() *huh.Form {
			opts := a.writers(d)
			if !slices.ContainsFunc(opts, func(o huh.Option[string]) bool { return o.Value == d.writer }) {
				d.writer = opts[0].Value
			}
			desc := roleNotes[roleWriter] + "\nPick where it runs; you pick the exact model next. Only providers ready to use are listed."
			if d.chosen().NeedsWriter {
				desc = d.chosen().ID + " only judges, so another model writes. " + desc
			}
			return huh.NewForm(huh.NewGroup(choose("Choose the writer", desc, opts, &d.writer)))
		}},
	)
	// The writer is picked like the judge was: its model, then how hard it thinks.
	steps = append(steps, a.modelSteps(d, [3]string{"Writer model", "Writer model id", "Writer effort"}, d.writerChoice,
		func() string { return roleWriter }, func() bool { return d.writerChoice().ID == "" })...)
	// Last, and only when research would otherwise have nothing to search with:
	// a writer's own web tool still works without it, slowly and at a price.
	steps = append(steps, step{title: "Research", skip: func() bool {
		s := a.searching(d)
		return !s.Enabled || s.With != ""
	}, build: func() *huh.Form {
		const why = "Before scoring, ideacheck looks the idea up on the web — who does this already, who tried and stopped, how big the market is — and needs something to search with. "
		if err := a.searching(d).DockerErr; err != nil {
			return huh.NewForm(huh.NewGroup(huh.NewNote().Title("Research has nothing to search with yet").
				// A note reads backticks as markup and cuts long lines off: plain, and folded here.
				Description(why + "The free way is a search engine of your own, which ideacheck runs in Docker.\n\n" + err.Error() +
					"\n\nAfter that, run: ideacheck search up\nUntil then, checks score the description alone, or use the writer's own web tool.").
				Next(true).NextLabel("Continue")))
		}
		d.startSearch = true
		return huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Start a free search engine for research?").
			Description(why + "Docker is running, so it can start its own SearXNG: free, no key, reachable from this machine only, about 250 MB once. `ideacheck search down` removes it.").
			Affirmative("Yes, start it").Negative("Not now").Value(&d.startSearch)))
	}})
	var cmd tea.Cmd
	a.wiz, cmd = newWizard("setup", a.width, a.height, steps)
	a.wiz.over = func() int { return lipgloss.Height(a.rolesLine(d)) - 1 }
	a.wiz.form = a.wiz.fit(a.wiz.form)
	return cmd
}

// modelSteps are the steps that pick one provider's model: the list, the typed
// id when the list does not have it, and the effort. who is the provider being
// picked for and role what its model will do; both can change as the user goes
// back and forth, so they are asked again whenever a step is built or counted.
func (a *App) modelSteps(d *setupDraft, titles [3]string, who func() config.Provider, role func() string, skip func() bool) []step {
	skipped := func() bool { return skip != nil && skip() }
	return []step{
		{title: titles[0], skip: skipped, build: func() *huh.Form {
			p := who()
			k := a.loadModels(d, p, role())
			if len(k.models) == 0 {
				// Blank is a real answer only for a provider that has its own default.
				desc := "The model id this provider should use.\n" + customHint(p)
				if p.Model == "" {
					desc = "The model id this provider should use. Leave blank for the provider's own default.\n" + k.note
				}
				return huh.NewForm(huh.NewGroup(customModel(k, modelQuestions[role()], strings.TrimSpace(roleNotes[role()]+"\n"+desc), p.Model != "")))
			}
			var opts []huh.Option[string]
			for _, m := range k.models {
				opts = append(opts, huh.NewOption(m.Label, m.ID))
			}
			other := "Other… (type a model id)"
			if p.Billing == "local" {
				other = "Other… (type any model id — downloaded for you if it is not here yet)"
			}
			opts = append(opts, huh.NewOption(other, otherModel))
			return huh.NewForm(huh.NewGroup(choose(modelQuestions[role()],
				strings.TrimSpace(roleNotes[role()]+"\n"+priceNote(p.Billing)+" "+k.note), opts, &k.model)))
		}},
		{title: titles[1], skip: func() bool {
			k := d.pickOf(who())
			return skipped() || len(k.models) == 0 || k.model != otherModel
		}, build: func() *huh.Form {
			return huh.NewForm(huh.NewGroup(customModel(d.pickOf(who()), "Model id", customHint(who()), true)))
		}},
		{title: titles[2], skip: func() bool {
			p := who()
			if k, ok := d.picks[p.ID]; ok {
				return skipped() || len(k.efforts(p)) == 0
			}
			return skipped() || len(p.Efforts) == 0
		}, build: func() *huh.Form {
			p := who()
			k := d.pickOf(p)
			// The CLI backends read "no effort" as thinking off; an API model keeps its own default.
			// Low is the advice for a judge only: each of its questions is one narrow
			// judgment, where a writer reads pages and writes paragraphs.
			cli, judges := backends.HumanOnly(p.Backend), role() != roleWriter
			opts := []huh.Option[string]{huh.NewOption("Default — the model decides", "")}
			switch {
			case cli && judges:
				opts[0] = huh.NewOption("Off — no thinking. Recommended: fastest, and each question is one narrow judgment", "")
			case cli:
				opts[0] = huh.NewOption("Off — no thinking: fastest", "")
			}
			for _, e := range k.efforts(p) {
				label := e
				if e == "low" && !cli && judges {
					label += " — recommended: each question is one short, narrow judgment"
				}
				opts = append(opts, huh.NewOption(label, e))
			}
			return huh.NewForm(huh.NewGroup(choose("How hard should "+cmp.Or(k.modelID(), "the model")+" think?",
				"Higher effort spends more tokens and more time.", opts, &k.effort)))
		}},
	}
}

// What a model being picked will do.
const (
	roleJudge  = "judge"
	roleWriter = "writer"
	roleBoth   = "judge and writer"
)

// role is what the judge's model will do: everything, when it writes as well.
func (d *setupDraft) role() string {
	if d.chosen().NeedsWriter || d.writerChoice().ID != "" {
		return roleJudge
	}
	return roleBoth
}

var modelQuestions = map[string]string{
	roleJudge:  "Which model should be the judge?",
	roleWriter: "Which model should be the writer?",
	roleBoth:   "Which model should judge and write?",
}

// roleNotes say what each role does, in the same words wherever it comes up.
var roleNotes = map[string]string{
	roleJudge:  "The judge answers every scoring question: whether the description states the problem, which kind of idea this is, each line of the rubric, and whether each research finding is about this idea. Every answer is a probability over a few named options; the judge never writes a word.",
	roleWriter: "The writer does what has no options to choose from: it reads your text for the details you did not type in, names the web searches and turns what they find into findings, and writes the summary paragraph. It never scores.",
	roleBoth:   "This model answers every scoring question, and also reads your text, researches the idea on the web and writes the summary.",
}

// rolesLine is the header's model line for the choices made so far, where
// every other page shows the saved ones — plus what research searches with,
// which this wizard can change too.
func (a *App) rolesLine(d *setupDraft) string {
	judge, writer := d.roles()
	w := ""
	switch {
	case writer.ID != "":
		w = who(writer.ID, d.modelOf(writer), "")
	case d.chosen().NeedsWriter:
		w = warnSty.Render("none") + dim.Render(" (scores only)")
	}
	search := dim.Render("off")
	switch s := a.searching(d); {
	case s.With != "":
		search = accent.Render(s.With)
	case s.Enabled && d.startSearch && s.DockerErr == nil:
		search = accent.Render("searxng") + dim.Render(" (starts when you finish)")
	case s.Enabled:
		search = dim.Render("the writer's own web tool")
	}
	return modelLine(formWidth(a.width), who(judge.ID, d.modelOf(judge), ""), w, search)
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
func customModel(k *pick, title, desc string, required bool) huh.Field {
	return huh.NewInput().Title(title).Description(desc).Value(&k.custom).Validate(func(s string) error {
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

// finishSetup downloads first whatever was picked that this machine runs and
// does not have yet — the chosen model, then the writer's — then saves, then
// starts the search engine if asked to.
func (a *App) finishSetup() tea.Cmd {
	d := a.setup
	for _, p := range []config.Provider{d.chosen(), d.writerChoice()} {
		if model := d.pickOf(p).modelID(); p.ID != "" && !d.fetched[model] && a.host.MissingModel(p, model) {
			return a.openDownload(p, model)
		}
	}
	return a.afterModel()
}

// afterModel saves the choices before anything else can go wrong: a search
// engine that does not start costs research, never the setup just made.
func (a *App) afterModel() tea.Cmd {
	if err := a.saveSetup(); err != nil {
		return a.fail(err)
	}
	if !a.setup.startSearch {
		return a.proceed()
	}
	return a.await("Starting your search engine", "a SearXNG in Docker; the first time downloads it", a.host.StartSearch,
		func(err error, stopped bool) tea.Cmd {
			cmd := a.proceed()
			switch {
			case stopped:
				a.note = "The search engine was not started. `ideacheck search up` starts it whenever you like."
			case err != nil:
				a.note = "Your settings are saved, but the search engine did not start.\n" + err.Error() + "\nTry again with `ideacheck search up`."
			}
			return cmd
		})
}

func (a *App) saveSetup() error {
	d := a.setup
	key := strings.TrimSpace(d.key)
	if a.key(d).Found() && !d.replaceKey {
		key = "" // typed, then gone back and chose to keep the one there
	}
	// The writer first: each save names its provider as the backend, and the
	// chosen one must be the name left standing should the roles not be saved.
	if w := d.writerChoice(); w.ID != "" {
		if k, picked := d.picks[w.ID]; picked {
			if err := a.host.SaveSetup(w, k.modelID(), k.saved(w), ""); err != nil {
				return err
			}
		}
	}
	p := d.chosen()
	if err := a.host.SaveSetup(p, d.pickOf(p).modelID(), d.pickOf(p).saved(p), key); err != nil {
		return err
	}
	return a.host.SaveRoles(d.roles())
}

// proceed leaves setup for wherever the user was headed.
func (a *App) proceed() tea.Cmd {
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

func (d *ideaDraft) intake(base ideacheck.Intake) ideacheck.Intake {
	out := ideacheck.Intake{Idea: strings.TrimSpace(d.idea), Fields: map[string]string{}, Profile: base.Profile}
	for name, v := range d.fields {
		if s := strings.TrimSpace(*v); s != "" {
			out.Fields[name] = s
		}
	}
	return out
}

func (a *App) openIdea() tea.Cmd {
	// One box is the default: the details are read out of the text, what the web
	// can answer is looked up, and only what is still missing gets asked.
	d := &ideaDraft{idea: a.intake.Idea, details: false, fields: map[string]*string{}}
	for _, name := range ideacheck.IdeaFields() {
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
				huh.NewText().Title("What is the idea?").Description("A sentence, a page, or a pasted document. Plain description beats a pitch.\nideacheck reads the details out of it, looks up what the web can answer, and asks only what is left.").Lines(6).Value(&d.idea).
					Validate(func(s string) error {
						if strings.TrimSpace(s) == "" {
							return errors.New("describe the idea")
						}
						return nil
					}),
				huh.NewConfirm().Title("Fill in the details yourself?").Description("Optional. Useful when the text above is short and you already know the problem, audience, competitors and timing.").
					Affirmative("Yes, step by step").Negative("No, just check it").Value(&d.details)))
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
	for _, name := range ideacheck.ProfileFields() {
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

func (a *App) openAsk(missing []ideacheck.Missing) tea.Cmd {
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
	a.engine = engine
	ctx, cancel := context.WithCancel(a.ctx)
	events := make(chan ideacheck.Event, 64)
	a.done, a.cancel = make(chan outcome, 1), cancel
	opts := a.start.Options
	opts.Events, opts.Proceed = events, opts.Proceed || a.asked // asked once: judge with what is known
	opts.Answered = append(opts.Answered, a.answered...)
	opts.Earlier = a.earlier
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
		a.live = a.live.apply(ideacheck.Event(msg))
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
		if got.res.Status == ideacheck.StatusNeedsInput && !a.asked && !a.start.NoAsk {
			a.earlier = got.res
			return a.openAsk(a.engine.FollowUps(got.res, a.intake, a.answered))
		}
		a.host.Persist(a.intake, got.res)
		a.result, a.tab, a.page = got.res, 0, pageResult
	}
	return nil
}

// ---- Result ----------------------------------------------------------------

var resultTabs = []string{"Overview", "All scores", "Evidence", "Gaps", "Details"}

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
		a.intake, a.asked, a.answered, a.earlier = ideacheck.Intake{}, false, nil, nil
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
			tabs = append(tabs, pill.Render(name))
		} else {
			tabs = append(tabs, dim.Padding(0, 1).Render(name))
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
		body += contributions("Top strengths", good, "▲", res.TopStrengths) + contributions("Top risks", bad, "▼", res.TopRisks)
		if line := costLine(res.Cost); line != "" {
			body += "\n" + line
		}
	case 1:
		body = ScoresView(res)
	case 2:
		body = evidenceTab(res)
	case 3:
		body = gapsView(res)
	case 4:
		body = detailsView(res)
	}
	return strings.Join(tabs, " ") + "\n\n" + body
}

// ScoresView lists every rubric dimension: the scores are the explanation.
func ScoresView(res *ideacheck.Result) string {
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
			fmt.Fprintf(&b, "%s %-28s %s %s  %s  %s\n", dim.Render(Arrow(d.Polarity)), d.ID, ColorBar(*d.Value, d.Polarity),
				scoreStyle(*d.Value, d.Polarity).Bold(true).Render(fmt.Sprintf("%.2f", *d.Value)), dim.Render(pct(d.Confidence)), dim.Render(fmt.Sprintf("weight %.1f", d.Weight)))
		}
	}
	return b.String() + "\n" + dim.Render("↑ higher is better · ↓ higher is worse · · informational (weight 0) · ") +
		good.Render("■") + dim.Render(" good news  ") + warnSty.Render("■") + dim.Render(" middling  ") + bad.Render("■") + dim.Render(" bad news")
}

func evidenceTab(res *ideacheck.Result) string {
	if res.Research == nil {
		return dim.Render("This check scored the description alone: nothing was looked up.\nResearch needs something to search with. The free way: `ideacheck search up` starts a search engine in Docker.\nOr set a TAVILY_API_KEY or BRAVE_API_KEY, or use a writer with its own web tool (Claude CLI, Codex CLI,\nAnthropic, OpenRouter). See `research:` in config.yaml.")
	}
	return EvidenceView(res.Research)
}

func gapsView(res *ideacheck.Result) string {
	if len(res.Missing) == 0 {
		return good.Render("✓") + " Nothing important is missing from the description."
	}
	return missingPanel(res.Missing)
}

func detailsView(res *ideacheck.Result) string {
	var b strings.Builder
	for _, w := range res.Warnings {
		b.WriteString(warnSty.Bold(true).Render("! ") + warnSty.Render(w) + "\n")
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
	a.history = table.New(table.WithFocused(true), table.WithStyles(tableStyles()), table.WithHeight(max(a.height-8, 5)), table.WithRows(trows), table.WithColumns([]table.Column{
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
