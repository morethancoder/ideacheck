package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/morethancoder/ideacheck/configs"
	"github.com/morethancoder/ideacheck/ideacheck"
	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/judge"
	"github.com/morethancoder/ideacheck/judge/mock"
	"github.com/morethancoder/ideacheck/store"
)

type fakeHost struct {
	t         *testing.T
	fixtures  string
	saved     []string
	roles     string // "judge+writer" provider ids, as SaveRoles got them
	keys      map[string]bool
	providers []config.Provider
	persists  []*ideacheck.Result
	profile   map[string]string
	current   [3]string // backend, model, effort the judge runs now; zero = mock
	writer    [3]string // backend, model, effort of the writer in effect
	notReady  string    // Engine fails with *NotReady until setup is saved
	models    []config.ModelChoice
	missing   string // MissingModel says true for this id
	pullErr   error  // what Download ends with
	hang      bool   // Download runs until it is stopped

	search       *SearchState // nil: research already has something to search with
	searchStarts int
	searchErr    error
	pulled       []string
}

func (h *fakeHost) Providers() []config.Provider {
	if h.providers != nil {
		return h.providers
	}
	return []config.Provider{{ID: "claude-cli", Backend: "claude-cli", Model: "sonnet"}, {ID: "openai", Backend: "structured", Provider: "openai", Model: "gpt-x", KeyEnv: "OPENAI_API_KEY"}}
}
func (h *fakeHost) Current() (string, string, string) {
	if h.current != [3]string{} {
		return h.current[0], h.current[1], h.current[2]
	}
	return "mock", "mock", ""
}
func (h *fakeHost) Models(p config.Provider, _ string) ([]config.ModelChoice, error) {
	if h.models != nil {
		return h.models, nil
	}
	return p.Models, nil
}
func (h *fakeHost) MissingModel(_ config.Provider, model string) bool {
	return model != "" && model == h.missing
}
func (h *fakeHost) Download(ctx context.Context, _ config.Provider, model string, progress func(Progress)) error {
	h.pulled = append(h.pulled, model)
	progress(Progress{Status: "pulling manifest"})
	progress(Progress{Status: "pulling abc", Done: 5e8, Total: 2e9})
	if h.hang {
		<-ctx.Done()
	}
	if h.pullErr != nil {
		return h.pullErr
	}
	progress(Progress{Status: "success", Done: 2e9, Total: 2e9})
	return ctx.Err()
}
func (h *fakeHost) Search() SearchState {
	if h.search == nil {
		return SearchState{Enabled: true, With: "searxng"} // something to search with: no step
	}
	return *h.search
}
func (h *fakeHost) StartSearch(_ context.Context, progress func(Progress)) error {
	h.searchStarts++
	progress(Progress{Status: "starting ideacheck-searxng"})
	return h.searchErr
}
func (h *fakeHost) Key(env string) Key {
	if h.keys[env] {
		return Key{From: "your environment", Tail: "abcd"}
	}
	return Key{}
}
func (h *fakeHost) HasCLI(string) bool { return true }
func (h *fakeHost) SaveSetup(p config.Provider, model, effort, key string) error {
	h.saved = append(h.saved, p.ID+"/"+model+"/"+effort+"/"+key)
	h.notReady = ""
	return nil
}
func (h *fakeHost) SaveRoles(judge, writer config.Provider) error {
	h.roles = judge.ID + "+" + writer.ID
	return nil
}
func (h *fakeHost) Writer() (string, string, string) { return h.writer[0], h.writer[1], h.writer[2] }
func (h *fakeHost) Engine() (*ideacheck.Engine, error) {
	if h.notReady != "" {
		return nil, &NotReady{Reason: h.notReady}
	}
	settings, err := ideacheck.DefaultSettings("mock", "")
	return &ideacheck.Engine{Settings: settings, Files: configs.Defaults(), Judge: &mock.Judge{Seed: 1, FixturesDir: h.fixtures}}, err
}
func (h *fakeHost) Profile() map[string]string               { return h.profile }
func (h *fakeHost) SaveProfile(p map[string]string) error    { h.profile = p; return nil }
func (h *fakeHost) History(int) ([]store.Row, error)         { return nil, nil }
func (h *fakeHost) Stored(string) (*ideacheck.Result, error) { return nil, nil }
func (h *fakeHost) Persist(_ ideacheck.Intake, r *ideacheck.Result) {
	h.persists = append(h.persists, r)
}

// pump runs a command chain the way the bubbletea runtime would, feeding every
// produced message back into Update, until the app leaves fromPage.
func pump(t *testing.T, a *App, cmd tea.Cmd, fromPage page) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0 && a.page == fromPage; steps++ {
		if steps > 2000 {
			t.Fatal("app never left the page")
		}
		next := queue[0]
		queue = queue[1:]
		if next == nil {
			continue
		}
		switch msg := next().(type) {
		case tea.BatchMsg:
			for _, c := range msg {
				if c != nil {
					queue = append(queue, c)
				}
			}
		case nil:
		default:
			if strings.HasSuffix(fmt.Sprintf("%T", msg), "TickMsg") {
				continue // spinner/cursor ticks re-arm forever; the flow does not need them
			}
			_, c := a.Update(msg)
			queue = append(queue, c)
		}
	}
}

// loadChosen lists the chosen provider's models, as its Model step does.
func loadChosen(a *App) *pick { return a.loadModels(a.setup, a.setup.chosen(), a.setup.role()) }

func newApp(h Host, start Start) *App {
	return &App{host: h, start: start, ctx: context.Background(), intake: start.Intake, width: 100, height: 30}
}

func TestCheckFlowAsksOnceThenShowsAndSavesTheResult(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "has_why_now.json"), []byte(`{"noul":0.2}`), 0o644)
	h := &fakeHost{t: t, fixtures: dir}
	a := newApp(h, Start{Page: pageLive, Intake: ideacheck.Intake{Idea: "A payroll tool", Profile: map[string]string{"background": "ran payroll at a restaurant"}}, Options: ideacheck.Options{Rubric: "business"}})

	pump(t, a, a.Init(), pageLive)
	if a.page != pageAsk || len(a.missing) != 1 || a.missing[0].ID != "has_why_now" || len(h.persists) != 0 {
		t.Fatalf("a gap must open the follow-up page before anything is saved: page=%v missing=%+v", a.page, a.missing)
	}
	if at, total := a.wiz.position(); at != 1 || total != 1 || !strings.Contains(a.View(), "What changed recently") {
		t.Errorf("ask wizard = step %d of %d\n%s", at, total, a.View())
	}

	a.replies[0] = "Tip-credit rules changed this year"
	a.page = pageAsk
	pump(t, a, a.wizardDone(), pageLive)
	if a.page != pageResult || a.result == nil || a.result.Status != ideacheck.StatusOK || a.result.Verdict == "" {
		t.Fatalf("after one round of questions the idea is judged regardless: page=%v result=%+v", a.page, a.result)
	}
	if a.intake.Fields["why_now"] != "Tip-credit rules changed this year" || len(h.persists) != 1 {
		t.Errorf("reply not stored or result not saved once: %+v persists=%d", a.intake.Fields, len(h.persists))
	}
	for tab, want := range []string{"Top strengths", "problem_acuity", "nothing was looked up", "", "mock values"} {
		a.tab = tab
		if view := a.View(); want != "" && !strings.Contains(view, want) {
			t.Errorf("tab %d (%s) missing %q", tab, resultTabs[tab], want)
		}
	}
	if a.tab = 3; strings.Contains(a.View(), "What changed recently") {
		t.Error("a gap the user has just answered must not be reported as missing")
	}
	a.tab = len(resultTabs) - 1
	a.Update(tea.KeyMsg{Type: tea.KeyRight})
	if a.tab != 0 {
		t.Errorf("→ from the last tab must wrap to the first, got %d", a.tab)
	}
}

// The details steps already asked for these fields: a blank one is an answer,
// so the follow-up page asks only about what was never shown (the background).
func TestDetailStepsAreNotAskedAgain(t *testing.T) {
	dir := t.TempDir()
	for _, gap := range []string{"has_why_now", "has_differentiation", "has_founder_context"} {
		_ = os.WriteFile(filepath.Join(dir, gap+".json"), []byte(`{"noul":0.2}`), 0o644)
	}
	h := &fakeHost{t: t, fixtures: dir}
	a := newApp(h, Start{Page: pageIdea, Options: ideacheck.Options{Rubric: "business"}})
	a.Init()
	a.draft.idea, a.draft.details = "A payroll tool", true // "Yes, step by step": one box is the default
	*a.draft.fields["why_now"] = "vague"
	pump(t, a, a.wizardDone(), pageIdea)
	pump(t, a, a.live.Init(), pageLive)
	if a.page != pageAsk || len(a.missing) != 1 || a.missing[0].Fills != "profile.background" {
		t.Fatalf("page=%v asks=%+v, want only the background question", a.page, a.missing)
	}

	a = newApp(h, Start{Page: pageIdea, Options: ideacheck.Options{Rubric: "business"}})
	a.Init()
	a.draft.idea, a.draft.details = "A payroll tool", false // "Skip, just check it"
	pump(t, a, a.wizardDone(), pageIdea)
	pump(t, a, a.live.Init(), pageLive)
	if a.page != pageAsk || len(a.missing) != 3 {
		t.Errorf("skipping the details must leave every gap to ask: page=%v asks=%d", a.page, len(a.missing))
	}
}

func TestNoAskReportsGapsWithoutAsking(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "has_why_now.json"), []byte(`{"noul":0.2}`), 0o644)
	a := newApp(&fakeHost{t: t, fixtures: dir}, Start{Page: pageLive, NoAsk: true, Intake: ideacheck.Intake{Idea: "x"}})
	pump(t, a, a.Init(), pageLive)
	if a.page != pageResult || a.result.Status != ideacheck.StatusNeedsInput {
		t.Errorf("-A: page=%v status=%q", a.page, a.result.Status)
	}
}

// A key that is already set is offered to keep, not an empty field to fill:
// keeping saves no key, replacing asks for the new one and saves it.
func TestSetupOffersToKeepAKeyAlreadySet(t *testing.T) {
	h := &fakeHost{t: t, keys: map[string]bool{"OPENAI_API_KEY": true}}
	a := newApp(h, Start{Page: pageSetup})
	a.Init()
	a.setup.id = "openai"
	a.wiz.move(1)
	if view := a.View(); !strings.Contains(view, "already set") || !strings.Contains(view, "ending in …abcd") || !strings.Contains(view, "Replace it") {
		t.Fatalf("want the keep-or-replace choice, got:\n%s", view)
	}
	if _, total := a.wiz.position(); total != 4 {
		t.Errorf("keeping the key: judge, key choice, judge model, writer (Claude CLI is ready too); total = %d", total)
	}
	a.setup.key, loadChosen(a).custom = "typed-then-kept", "gpt-y"
	a.finishSetup()
	if len(h.saved) != 1 || h.saved[0] != "openai/gpt-y//" {
		t.Errorf("keeping the key must save none: %v", h.saved)
	}

	h.saved = nil
	a = newApp(h, Start{Page: pageSetup})
	a.Init()
	a.setup.id, a.setup.replaceKey = "openai", true
	a.wiz.move(1)
	a.wiz.move(1)
	if view := a.View(); !strings.Contains(view, "OPENAI_API_KEY") || !strings.Contains(view, "that one wins") {
		t.Fatalf("replacing a key from the environment must ask for it and say the environment wins:\n%s", view)
	}
	a.setup.key, loadChosen(a).custom = " sk-new ", "gpt-y"
	a.finishSetup()
	if len(h.saved) != 1 || h.saved[0] != "openai/gpt-y//sk-new" {
		t.Errorf("saved = %v", h.saved)
	}
}

func TestFirstRunSetupThenContinuesToTheCheck(t *testing.T) {
	h := &fakeHost{t: t}
	a := newApp(h, Start{Page: pageLive, NeedSetup: true, Intake: ideacheck.Intake{Idea: "x"}})
	a.Init()
	if a.page != pageSetup {
		t.Fatalf("first run must open setup, got page %v", a.page)
	}
	if at, total := a.wiz.position(); at != 1 || total != 2 {
		t.Errorf("claude-cli needs no key: want step 1 of 2, got %d of %d", at, total)
	}
	a.setup.id = "openai"
	if _, total := a.wiz.position(); total != 4 {
		t.Errorf("a key provider adds the API key step, and Claude CLI is there to write: total = %d", total)
	}
	a.setup.key, loadChosen(a).custom = " sk-test ", " gpt-y "
	a.finishSetup()
	if len(h.saved) != 1 || h.saved[0] != "openai/gpt-y//sk-test" {
		t.Errorf("saved = %v", h.saved)
	}
	if a.page != pageLive || a.start.NeedSetup {
		t.Errorf("after first-run setup the app must continue to the check: page=%v", a.page)
	}
}

func TestUnreachableModelOpensSettingsCalmlyThenRunsTheCheck(t *testing.T) {
	h := &fakeHost{t: t, notReady: "The model qwen3:8b is not downloaded."}
	a := newApp(h, Start{Page: pageLive, Intake: ideacheck.Intake{Idea: "x"}})
	a.Init()
	view := a.View()
	if a.page != pageSetup || a.banner != "" || !strings.Contains(view, "qwen3:8b is not downloaded") || strings.Contains(view, "! ") {
		t.Fatalf("want Settings with a calm note, not an error: page=%v banner=%q\n%s", a.page, a.banner, view)
	}
	loadChosen(a).custom = "llama3.2:3b"
	a.finishSetup()
	if a.page != pageLive || a.note != "" || a.intake.Idea != "x" {
		t.Errorf("after fixing settings the same check must run: page=%v note=%q idea=%q", a.page, a.note, a.intake.Idea)
	}
}

func TestSetupNeverDefaultsToAModelThatIsNotInstalled(t *testing.T) {
	ollama := config.Provider{ID: "ollama", Backend: "logprob", Model: "qwen3:8b", Discover: "ollama"}
	h := &fakeHost{t: t, providers: []config.Provider{ollama}, models: []config.ModelChoice{{ID: "llama3.2:3b"}, {ID: "gemma3:4b"}}}
	a := newApp(h, Start{Page: pageMenu})
	a.open(pageSetup)
	if got := loadChosen(a).modelID(); got != "llama3.2:3b" {
		t.Errorf("default model = %q, want the first installed one (qwen3:8b is not pulled)", got)
	}
	h.models = []config.ModelChoice{{ID: "gemma3:4b"}, {ID: "qwen3:8b"}}
	a.open(pageSetup)
	if got := loadChosen(a).modelID(); got != "qwen3:8b" {
		t.Errorf("default model = %q, want the preset when it is installed", got)
	}
}

func TestSetupKeepsAPinnedModelTheLiveListNoLongerNames(t *testing.T) {
	jev := config.Provider{ID: "jev", Backend: "jev", Model: "jev-latest", Discover: "typesafe",
		Models: []config.ModelChoice{{ID: "jev-latest"}, {ID: "jev-1.13.0"}}}
	h := &fakeHost{t: t, providers: []config.Provider{jev}, models: []config.ModelChoice{{ID: "jev-preview"}, {ID: "jev-latest"}},
		current: [3]string{"jev", "jev-1.13.0", ""}}
	a := newApp(h, Start{Page: pageMenu})
	a.open(pageSetup)
	if got := loadChosen(a).modelID(); got != "jev-1.13.0" {
		t.Errorf("model = %q, want the saved pin kept", got)
	}
	h.current = [3]string{"jev", "retired-model", ""}
	a.open(pageSetup)
	if got := loadChosen(a).modelID(); got != "jev-preview" {
		t.Errorf("model = %q, want the newest listed: a saved id the presets do not name is not a pin", got)
	}
}

func TestSetupOffersModelsThenEffort(t *testing.T) {
	h := &fakeHost{t: t, providers: []config.Provider{{ID: "claude-cli", Backend: "claude-cli", Model: "sonnet", Efforts: []string{"low", "high"},
		Models: []config.ModelChoice{{ID: "sonnet", Label: "Sonnet"}, {ID: "haiku", Label: "Haiku", NoEffort: true}}}}}
	a := newApp(h, Start{Page: pageMenu})
	a.open(pageSetup)
	d := loadChosen(a)
	if d.model != "sonnet" || d.effort != "low" {
		t.Errorf("defaults: model=%q effort=%q, want the provider default at low effort", d.model, d.effort)
	}
	if _, total := a.wiz.position(); total != 3 {
		t.Errorf("provider, model, effort: total = %d", total)
	}
	d.model = "haiku"
	if _, total := a.wiz.position(); total != 2 {
		t.Errorf("a model without effort control skips the effort step: total = %d", total)
	}
	a.finishSetup()
	d.model, d.custom = otherModel, " claude-x "
	if _, total := a.wiz.position(); total != 4 {
		t.Errorf("Other… adds the model id step: total = %d", total)
	}
	a.finishSetup()
	if len(h.saved) != 2 || h.saved[0] != "claude-cli/haiku//" || h.saved[1] != "claude-cli/claude-x/low/" {
		t.Errorf("saved = %v", h.saved)
	}
}

func TestWizardSkipsAndGoesBack(t *testing.T) {
	skipMiddle := true
	form := func() *huh.Form { v := ""; return huh.NewForm(huh.NewGroup(huh.NewInput().Value(&v))) }
	w, _ := newWizard("t", 80, 24, []step{{title: "one", build: form}, {title: "two", build: form, skip: func() bool { return skipMiddle }}, {title: "three", build: form}})
	if state, _ := w.move(1); state != wizardRunning || w.steps[w.at].title != "three" {
		t.Fatalf("forward must skip step two: at %q", w.steps[w.at].title)
	}
	if state, _ := w.move(1); state != wizardDone {
		t.Error("past the last step = done")
	}
	if state, _ := w.update(tea.KeyMsg{Type: tea.KeyEsc}); state != wizardRunning || w.steps[w.at].title != "one" {
		t.Errorf("esc must go back over the skipped step: at %q", w.steps[w.at].title)
	}
	if state, _ := w.update(tea.KeyMsg{Type: tea.KeyEsc}); state != wizardLeft {
		t.Error("esc on the first step leaves the wizard")
	}
}

func TestMenuNavigation(t *testing.T) {
	a := newApp(&fakeHost{t: t}, Start{Page: pageMenu})
	a.Init()
	a.Update(tea.KeyMsg{Type: tea.KeyUp})
	if menuItems[a.cursor].label != "Quit" {
		t.Errorf("↑ from the top wraps to the bottom, got %q", menuItems[a.cursor].label)
	}
	a.cursor = 3 // Settings
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.page != pageSetup || !strings.Contains(a.View(), "Step 1 of 2") {
		t.Errorf("Settings must open the setup wizard: page=%v", a.page)
	}
	_ = judge.Noul
}

func TestSetupDownloadsAMissingLocalModelBeforeSavingIt(t *testing.T) {
	ollama := config.Provider{ID: "ollama", Backend: "logprob", Model: "qwen3:8b", Discover: "ollama", Billing: "local"}
	h := &fakeHost{t: t, providers: []config.Provider{ollama}, models: []config.ModelChoice{{ID: "llama3.2:3b"}}, missing: "qwen3:8b"}
	a := newApp(h, Start{Page: pageLive, NeedSetup: true, Intake: ideacheck.Intake{Idea: "x"}})
	a.Init()
	k := loadChosen(a)
	k.model, k.custom = otherModel, " qwen3:8b "

	cmd := a.finishSetup()
	if a.page != pageDownload || len(h.saved) != 0 {
		t.Fatalf("a model that is not here must be downloaded before it is saved: page=%v saved=%v", a.page, h.saved)
	}
	_, cmd = a.Update(cmd()) // pulling manifest
	_, cmd = a.Update(cmd()) // a quarter done
	if view := a.View(); !strings.Contains(view, "Downloading qwen3:8b") || !strings.Contains(view, "25%  476.8 MB / 1.9 GB") {
		t.Errorf("download page:\n%s", view)
	}
	pump(t, a, cmd, pageDownload)
	if len(h.pulled) != 1 || h.pulled[0] != "qwen3:8b" || len(h.saved) != 1 || h.saved[0] != "ollama/qwen3:8b//" || a.page != pageLive {
		t.Errorf("after the download: pulled=%v saved=%v page=%v, want saved and on to the check", h.pulled, h.saved, a.page)
	}
}

func TestAFailedDownloadGoesBackToSetupWithoutSaving(t *testing.T) {
	ollama := config.Provider{ID: "ollama", Backend: "logprob", Model: "qwen3:8b", Discover: "ollama", Billing: "local"}
	h := &fakeHost{t: t, providers: []config.Provider{ollama}, missing: "nope:1b", pullErr: errors.New("Ollama has no model called nope:1b")}
	a := newApp(h, Start{Page: pageMenu})
	a.open(pageSetup)
	k := loadChosen(a)
	k.model, k.custom = otherModel, "nope:1b"
	pump(t, a, a.finishSetup(), pageDownload)
	if a.page != pageSetup || len(h.saved) != 0 || !strings.Contains(a.note, "no model called nope:1b") || a.banner != "" {
		t.Errorf("page=%v saved=%v note=%q banner=%q", a.page, h.saved, a.note, a.banner)
	}

	h.missing, h.pullErr, h.hang = "big:70b", nil, true
	k = loadChosen(a) // going back opened a fresh setup
	k.model, k.custom = otherModel, "big:70b"
	cmd := a.finishSetup()
	a.Update(tea.KeyMsg{Type: tea.KeyEsc}) // stop it
	pump(t, a, cmd, pageDownload)
	if a.page != pageSetup || len(h.saved) != 0 || !strings.Contains(a.note, "Download stopped") {
		t.Errorf("esc: page=%v saved=%v note=%q", a.page, h.saved, a.note)
	}
}

func TestAnInstalledModelIsSavedWithoutDownloading(t *testing.T) {
	h := &fakeHost{t: t, missing: "other"}
	a := newApp(h, Start{Page: pageMenu})
	a.open(pageSetup)
	loadChosen(a)
	a.finishSetup()
	if len(h.pulled) != 0 || len(h.saved) != 1 {
		t.Errorf("pulled=%v saved=%v", h.pulled, h.saved)
	}
}

// The bug this guards: huh sizes a group's viewport when the group is built,
// at its own default width. A description that wraps to one more line at the
// real width then pushed the input out of view — the step showed its title and
// description, and typing did nothing.
func TestAStepYouCanReadIsAStepYouCanTypeInto(t *testing.T) {
	d := &pick{custom: "typed-so-far"}
	long := "Any model from ollama.com/library, e.g. qwen3:8b. If it is not on this machine yet, it is downloaded when you finish."
	w, _ := newWizard("t", 100, 30, []step{{title: "Model id", build: func() *huh.Form {
		return huh.NewForm(huh.NewGroup(customModel(d, "Model id", long, true)))
	}}})
	view := w.view()
	if !strings.Contains(view, "typed-so-far") {
		t.Errorf("the field is clipped out of the form's viewport:\n%s", view)
	}
	if h := lipgloss.Height(view); h > 12 {
		t.Errorf("a short step must not pad to the whole terminal: %d lines\n%s", h, view)
	}
}

func TestABlankModelIDIsNotSaved(t *testing.T) {
	submit := func(id string) []error {
		d := &pick{custom: id}
		form := huh.NewForm(huh.NewGroup(customModel(d, "Model id", "", true))).WithWidth(60)
		form.Init()
		m, _ := form.Update(tea.KeyMsg{Type: tea.KeyEnter})
		return m.(*huh.Form).Errors()
	}
	if len(submit("")) == 0 || len(submit("  ")) == 0 {
		t.Error("enter on an empty id must not be accepted: that saved a model that is no model")
	}
	if errs := submit("qwen3:8b"); len(errs) != 0 {
		t.Errorf("a typed id is accepted: %v", errs)
	}
}

// Setup asks for the judge, then for the writer. A judge that only judges
// (Jev, Laya) needs another provider or nobody; a chat judge can write as well,
// or hand that to another provider. The saved roles say who does what.
func TestSetupPairsAJudgeWithAWriter(t *testing.T) {
	providers := []config.Provider{
		{ID: "claude-cli", Label: "Claude CLI", Backend: "claude-cli", NeedsCLI: "claude", Model: "sonnet"},
		{ID: "jev", Label: "Jev", Backend: "jev", KeyEnv: "TYPESAFE_API_KEY", NeedsWriter: true, Model: "jev-1"},
		{ID: "laya", Label: "Laya", Backend: "laya", NeedsWriter: true, Model: "aac6fef/laya-mlx"},
		{ID: "openai", Label: "OpenAI", Backend: "structured", KeyEnv: "OPENAI_API_KEY", Model: "gpt"},
	}
	h := &fakeHost{t: t, providers: providers, keys: map[string]bool{"TYPESAFE_API_KEY": true}}
	a := newApp(h, Start{Page: pageSetup, ExitAfter: true})
	a.Init()
	if view := a.View(); !strings.Contains(view, "Choose the judge") || !strings.Contains(view, "every scoring question") {
		t.Errorf("the first step names the role and what it does:\n%s", view)
	}

	for _, id := range []string{"jev", "laya"} {
		a.setup.id = id
		if opts := a.writers(a.setup); len(opts) != 2 || opts[0].Value != "claude-cli" || opts[1].Value != noWriter {
			t.Fatalf("%s: writers = %+v, want the ready chat provider then nobody (OpenAI has no key; a judge-only model never writes)", id, opts)
		}
		a.setup.writer = "claude-cli"
		a.saveSetup()
		if h.roles != id+"+claude-cli" {
			t.Errorf("roles = %q, want %s judging and claude-cli writing", h.roles, id)
		}
		a.setup.writer = noWriter
		a.saveSetup()
		if h.roles != id+"+" {
			t.Errorf("roles = %q, want %s alone", h.roles, id)
		}
	}

	a.setup.id = "claude-cli"
	opts := a.writers(a.setup)
	if len(opts) != 1 || opts[0].Value != sameWriter {
		t.Fatalf("writers = %+v, want only the judge itself (nothing else is ready, and a chat judge needs nobody)", opts)
	}
	if _, total := a.wiz.position(); total != 2 {
		t.Errorf("one possible writer is no choice: judge, judge model; total = %d", total)
	}
	a.setup.writer = sameWriter
	a.saveSetup()
	if h.roles != "claude-cli+" {
		t.Errorf("roles = %q, want one model doing everything", h.roles)
	}

	h.keys["OPENAI_API_KEY"] = true
	if opts := a.writers(a.setup); len(opts) != 2 || opts[1].Value != "openai" {
		t.Fatalf("writers = %+v, want the judge itself, then OpenAI now that it has a key", opts)
	}
	if _, total := a.wiz.position(); total != 3 {
		t.Errorf("a second possible writer adds the Writer step: total = %d", total)
	}
	a.setup.writer = "openai"
	a.saveSetup()
	if h.roles != "claude-cli+openai" {
		t.Errorf("roles = %q, want a chat judge with another writer", h.roles)
	}
}

// The writer is picked like the judge: how to reach it, then its model and its
// effort. It was once a provider at its preset model, with no way to pick
// another — and Settings opened on "none" whatever writer was configured.
func TestSetupPicksTheWritersModel(t *testing.T) {
	providers := []config.Provider{
		{ID: "jev", Label: "Jev", Backend: "jev", KeyEnv: "TYPESAFE_API_KEY", NeedsWriter: true, Model: "jev-1"},
		{ID: "ollama", Label: "Ollama", Backend: "logprob", Model: "qwen3:8b"},
		{ID: "claude-cli", Label: "Claude CLI", Backend: "claude-cli", Model: "sonnet", Efforts: []string{"low", "high"},
			Models: []config.ModelChoice{{ID: "sonnet", Label: "Sonnet"}, {ID: "haiku", Label: "Haiku", NoEffort: true}}},
	}
	h := &fakeHost{t: t, providers: providers, keys: map[string]bool{"TYPESAFE_API_KEY": true}, writer: [3]string{"claude-cli", "haiku", ""}}
	a := newApp(h, Start{Page: pageSetup, ExitAfter: true})
	a.Init()
	d := a.setup
	d.id = "jev"
	if view := a.View(); !strings.Contains(view, "writer claude-cli · haiku") {
		t.Errorf("settings must open on the writer configured now:\n%s", view)
	}
	k := a.loadModels(d, d.writerChoice(), roleWriter)
	if k.modelID() != "haiku" {
		t.Errorf("the writer's model defaults to the one it runs now, got %q", k.modelID())
	}
	if _, total := a.wiz.position(); total != 5 {
		t.Errorf("judge, key, judge model, writer, writer model (haiku takes no effort): total = %d", total)
	}
	k.model, k.effort = "sonnet", "high"
	if _, total := a.wiz.position(); total != 6 {
		t.Errorf("sonnet adds the writer's effort step: total = %d", total)
	}
	if view := a.View(); !strings.Contains(view, "writer claude-cli · sonnet") {
		t.Errorf("the header follows the choice being made:\n%s", view)
	}
	a.saveSetup()
	if len(h.saved) != 2 || h.saved[0] != "claude-cli/sonnet/high/" || !strings.HasPrefix(h.saved[1], "jev/") || h.roles != "jev+claude-cli" {
		t.Errorf("saved = %v roles = %q, want the writer's model, then the judge", h.saved, h.roles)
	}

	d.writer = "ollama" // never listed: a writer new to this config starts at its own effort, not the judge's low
	if k := a.loadModels(d, d.writerChoice(), roleWriter); k.modelID() != "qwen3:8b" || k.effort != "" {
		t.Errorf("a new writer: model=%q effort=%q, want its preset at its own effort", k.modelID(), k.effort)
	}
	d.writer = noWriter
	if _, total := a.wiz.position(); total != 4 {
		t.Errorf("nobody writes, so there is no writer model to pick: total = %d", total)
	}
}

// researchStep opens setup and walks the wizard straight to its Research step;
// ok is false when the wizard does not show one.
func researchStep(t *testing.T, a *App) (view string, ok bool) {
	t.Helper()
	a.Init()
	for i, s := range a.wiz.steps {
		if s.title == "Research" {
			if !a.wiz.shown(i) {
				return "", false
			}
			a.wiz.at = i - 1
			a.wiz.move(1)
			return a.wiz.view(), true
		}
	}
	t.Fatal("setup has no Research step")
	return "", false
}

func TestSetupOffersASearchEngineOnlyWhenResearchHasNone(t *testing.T) {
	ollama := config.Provider{ID: "ollama", Backend: "logprob", Model: "qwen3:8b", Billing: "local"}
	for name, state := range map[string]*SearchState{"something already searches": nil, "research is off": {Enabled: false}} {
		h := &fakeHost{t: t, providers: []config.Provider{ollama}, search: state}
		if _, ok := researchStep(t, newApp(h, Start{Page: pageSetup, NeedSetup: true})); ok {
			t.Errorf("%s: the step has nothing to offer", name)
		}
	}
	h := &fakeHost{t: t, providers: []config.Provider{ollama}, search: &SearchState{Enabled: true}}
	view, ok := researchStep(t, newApp(h, Start{Page: pageSetup, NeedSetup: true}))
	if !ok || !strings.Contains(view, "Start a free search engine") || !strings.Contains(view, "Yes, start it") {
		t.Errorf("with Docker ready and nothing to search with:\n%s", view)
	}
	if !strings.Contains(view, "removes it") {
		t.Errorf("the explanation must wrap, not be cut off at the edge:\n%s", view)
	}
}

// Without Docker the step cannot offer anything, so it says how to get there.
func TestSetupSaysHowToGetDockerWhenItCannotStartASearchEngine(t *testing.T) {
	ollama := config.Provider{ID: "ollama", Backend: "logprob", Model: "qwen3:8b", Billing: "local"}
	h := &fakeHost{t: t, providers: []config.Provider{ollama},
		search: &SearchState{Enabled: true, DockerErr: errors.New("Docker is installed but not running\nStart it: open -a Docker")}}
	a := newApp(h, Start{Page: pageLive, NeedSetup: true, Intake: ideacheck.Intake{Idea: "x"}})
	view, ok := researchStep(t, a)
	if !ok || !strings.Contains(view, "open -a Docker") || !strings.Contains(view, "ideacheck search up") || !strings.Contains(view, "own web tool") {
		t.Fatalf("the step must carry the way out:\n%s", view)
	}
	a.finishSetup()
	if h.searchStarts != 0 || len(h.saved) != 1 {
		t.Errorf("nothing to start, settings saved: starts=%d saved=%v", h.searchStarts, h.saved)
	}
}

func TestSetupStartsTheSearchEngineAfterSaving(t *testing.T) {
	ollama := config.Provider{ID: "ollama", Backend: "logprob", Model: "qwen3:8b", Billing: "local"}
	h := &fakeHost{t: t, providers: []config.Provider{ollama}, search: &SearchState{Enabled: true}}
	a := newApp(h, Start{Page: pageLive, NeedSetup: true, Intake: ideacheck.Intake{Idea: "x"}})
	a.Init()
	a.setup.startSearch = true
	cmd := a.finishSetup()
	if a.page != pageDownload || len(h.saved) != 1 || !strings.Contains(a.View(), "Starting your search engine") {
		t.Fatalf("saved first, then the progress page: page=%v saved=%v\n%s", a.page, h.saved, a.View())
	}
	pump(t, a, cmd, pageDownload)
	if h.searchStarts != 1 || a.page != pageLive || a.note != "" {
		t.Errorf("starts=%d page=%v note=%q", h.searchStarts, a.page, a.note)
	}
}

// A search engine that will not start costs research, never the setup.
func TestAFailedSearchEngineStartKeepsTheSetup(t *testing.T) {
	ollama := config.Provider{ID: "ollama", Backend: "logprob", Model: "qwen3:8b", Billing: "local"}
	h := &fakeHost{t: t, providers: []config.Provider{ollama}, search: &SearchState{Enabled: true}, searchErr: errors.New("port 8080 is taken")}
	a := newApp(h, Start{Page: pageLive, NeedSetup: true, Intake: ideacheck.Intake{Idea: "x"}})
	a.Init()
	a.setup.startSearch = true
	pump(t, a, a.finishSetup(), pageDownload)
	if len(h.saved) != 1 || a.page != pageLive || !strings.Contains(a.note, "port 8080 is taken") || !strings.Contains(a.note, "ideacheck search up") {
		t.Errorf("saved=%v page=%v note=%q", h.saved, a.page, a.note)
	}
}

// A first run with nothing but a local model answers the Writer step for the
// user: the judge writes too. Nothing skipped may leave the roles unset.
func TestFirstRunWithOneProviderSavesItAsJudgeAndWriter(t *testing.T) {
	ollama := config.Provider{ID: "ollama", Backend: "logprob", Model: "qwen3:8b", Billing: "local"}
	jev := config.Provider{ID: "jev", Backend: "jev", Model: "jev-1", KeyEnv: "TYPESAFE_API_KEY", NeedsWriter: true}
	h := &fakeHost{t: t, providers: []config.Provider{ollama, jev}}
	a := newApp(h, Start{Page: pageSetup, NeedSetup: true})
	a.Init()
	a.setup.id = "ollama"
	if _, total := a.wiz.position(); total != 2 {
		t.Errorf("judge, judge model — Jev has no key, so no writer to choose: total = %d", total)
	}
	if judge, writer := a.setup.roles(); judge.ID != "ollama" || writer.ID != "" || a.setup.role() != roleBoth {
		t.Errorf("judge=%q writer=%q role=%q, want ollama alone", judge.ID, writer.ID, a.setup.role())
	}
}

// A list shows every option whatever is chosen, before and after a key. It
// once lost them on the first key: the form had been measured before huh
// wrapped the description at the window's width, came out too short, and huh
// squeezed the list into a window that starts at the cursor — with the last
// option chosen, the only one left on screen.
func TestAListShowsEveryOptionWhateverIsChosen(t *testing.T) {
	desc := "Keep the key you have, or replace it with another one that is long enough to wrap onto a second line."
	value := "e"
	w, _ := newWizard("t", 80, 24, []step{{title: "Pick", build: func() *huh.Form {
		opts := []huh.Option[string]{huh.NewOption("Alpha", "a"), huh.NewOption("Bravo", "b"), huh.NewOption("Charlie", "c"), huh.NewOption("Delta", "d"), huh.NewOption("Echo", "e")}
		return huh.NewForm(huh.NewGroup(choose("Which one?", desc, opts, &value)))
	}}})
	for _, key := range []tea.KeyType{tea.KeyUp, tea.KeyDown} {
		w.update(tea.KeyMsg{Type: key})
		view := w.view()
		for _, want := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo", "second line."} {
			if !strings.Contains(view, want) {
				t.Errorf("lost %q:\n%s", want, view)
			}
		}
	}
}

// Every settings step wraps to the window: nothing is cut off at the edge.
func TestSettingsWrapToTheWindow(t *testing.T) {
	providers := []config.Provider{
		{ID: "claude-cli", Label: "Claude CLI — use my Claude login (no API key)", Backend: "claude-cli", Model: "sonnet"},
		{ID: "jev", Label: "Jev", Backend: "jev", KeyEnv: "TYPESAFE_API_KEY", NeedsWriter: true, Model: "jev-1"},
	}
	h := &fakeHost{t: t, providers: providers, keys: map[string]bool{"TYPESAFE_API_KEY": true}, search: &SearchState{Enabled: true}}
	a := newApp(h, Start{Page: pageMenu})
	a.width, a.height = 60, 24
	a.open(pageSetup)
	for state := wizardRunning; state == wizardRunning; state, _ = a.wiz.move(1) {
		view := a.View()
		for _, line := range strings.Split(view, "\n") {
			if w := lipgloss.Width(strings.TrimRight(line, " ")); w > a.width {
				t.Errorf("%s: a line of %d cells in a %d-cell window:\n%s", a.wiz.steps[a.wiz.at].title, w, a.width, view)
				break
			}
		}
		if h := lipgloss.Height(view); h > a.height {
			t.Errorf("%s: %d lines in a %d-line window:\n%s", a.wiz.steps[a.wiz.at].title, h, a.height, view)
		}
	}
}
