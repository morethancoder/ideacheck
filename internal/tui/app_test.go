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

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/judge/backends/mock"
	"github.com/morethancoder/ideacheck/internal/pipeline"
	"github.com/morethancoder/ideacheck/internal/store"
)

type fakeHost struct {
	t         *testing.T
	fixtures  string
	saved     []string
	providers []config.Provider
	persists  []*pipeline.Result
	profile   map[string]string
	notReady  string // Engine fails with *NotReady until setup is saved
	models    []config.ModelChoice
	missing   string // MissingModel says true for this id
	pullErr   error  // what Download ends with
	hang      bool   // Download runs until it is stopped
	pulled    []string
}

func (h *fakeHost) Providers() []config.Provider {
	if h.providers != nil {
		return h.providers
	}
	return []config.Provider{{ID: "claude-cli", Backend: "claude-cli", Model: "sonnet"}, {ID: "openai", Backend: "structured", Provider: "openai", Model: "gpt-x", KeyEnv: "OPENAI_API_KEY"}}
}
func (h *fakeHost) Current() (string, string, string) { return "mock", "mock", "" }
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
func (h *fakeHost) HasKey(string) bool { return false }
func (h *fakeHost) HasCLI(string) bool { return true }
func (h *fakeHost) SaveSetup(p config.Provider, model, effort, key string) error {
	h.saved = append(h.saved, p.ID+"/"+model+"/"+effort+"/"+key)
	h.notReady = ""
	return nil
}
func (h *fakeHost) Engine() (*pipeline.Engine, error) {
	if h.notReady != "" {
		return nil, &NotReady{Reason: h.notReady}
	}
	files := config.NewFiles("")
	cfg, err := config.Load(files, config.LoadOptions{Environ: func() []string { return nil }, Overrides: map[string]any{"backend": "mock"}})
	return &pipeline.Engine{Config: cfg, Files: files, Judge: &mock.Judge{Seed: 1, FixturesDir: h.fixtures}}, err
}
func (h *fakeHost) Profile() map[string]string                    { return h.profile }
func (h *fakeHost) SaveProfile(p map[string]string) error         { h.profile = p; return nil }
func (h *fakeHost) History(int) ([]store.Row, error)              { return nil, nil }
func (h *fakeHost) Stored(string) (*pipeline.Result, error)       { return nil, nil }
func (h *fakeHost) Persist(_ pipeline.Intake, r *pipeline.Result) { h.persists = append(h.persists, r) }

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

func newApp(h Host, start Start) *App {
	return &App{host: h, start: start, ctx: context.Background(), intake: start.Intake, width: 100, height: 30}
}

func TestCheckFlowAsksOnceThenShowsAndSavesTheResult(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "has_why_now.json"), []byte(`{"noul":0.2}`), 0o644)
	h := &fakeHost{t: t, fixtures: dir}
	a := newApp(h, Start{Page: pageLive, Intake: pipeline.Intake{Idea: "A payroll tool"}, Options: pipeline.Options{Rubric: "business"}})

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
	if a.page != pageResult || a.result == nil || a.result.Status != pipeline.StatusOK || a.result.Verdict == "" {
		t.Fatalf("after one round of questions the idea is judged regardless: page=%v result=%+v", a.page, a.result)
	}
	if a.intake.Fields["why_now"] != "Tip-credit rules changed this year" || len(h.persists) != 1 {
		t.Errorf("reply not stored or result not saved once: %+v persists=%d", a.intake.Fields, len(h.persists))
	}
	for tab, want := range []string{"Top strengths", "problem_acuity", "", "mock values"} {
		a.tab = tab
		if view := a.View(); want != "" && !strings.Contains(view, want) {
			t.Errorf("tab %d (%s) missing %q", tab, resultTabs[tab], want)
		}
	}
	if a.tab = 2; strings.Contains(a.View(), "What changed recently") {
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
	a := newApp(h, Start{Page: pageIdea, Options: pipeline.Options{Rubric: "business"}})
	a.Init()
	a.draft.idea = "A payroll tool"
	*a.draft.fields["why_now"] = "vague"
	pump(t, a, a.wizardDone(), pageIdea)
	pump(t, a, a.live.Init(), pageLive)
	if a.page != pageAsk || len(a.missing) != 1 || a.missing[0].Fills != "profile.background" {
		t.Fatalf("page=%v asks=%+v, want only the background question", a.page, a.missing)
	}

	a = newApp(h, Start{Page: pageIdea, Options: pipeline.Options{Rubric: "business"}})
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
	a := newApp(&fakeHost{t: t, fixtures: dir}, Start{Page: pageLive, NoAsk: true, Intake: pipeline.Intake{Idea: "x"}})
	pump(t, a, a.Init(), pageLive)
	if a.page != pageResult || a.result.Status != pipeline.StatusNeedsInput {
		t.Errorf("-A: page=%v status=%q", a.page, a.result.Status)
	}
}

func TestFirstRunSetupThenContinuesToTheCheck(t *testing.T) {
	h := &fakeHost{t: t}
	a := newApp(h, Start{Page: pageLive, NeedSetup: true, Intake: pipeline.Intake{Idea: "x"}})
	a.Init()
	if a.page != pageSetup {
		t.Fatalf("first run must open setup, got page %v", a.page)
	}
	if at, total := a.wiz.position(); at != 1 || total != 2 {
		t.Errorf("claude-cli needs no key: want step 1 of 2, got %d of %d", at, total)
	}
	a.setup.id = "openai"
	if _, total := a.wiz.position(); total != 3 {
		t.Errorf("a key provider adds the API key step: total = %d", total)
	}
	a.setup.key, a.setup.custom = " sk-test ", " gpt-y "
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
	a := newApp(h, Start{Page: pageLive, Intake: pipeline.Intake{Idea: "x"}})
	a.Init()
	view := a.View()
	if a.page != pageSetup || a.banner != "" || !strings.Contains(view, "qwen3:8b is not downloaded") || strings.Contains(view, "! ") {
		t.Fatalf("want Settings with a calm note, not an error: page=%v banner=%q\n%s", a.page, a.banner, view)
	}
	a.setup.custom = "llama3.2:3b"
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
	a.loadModels(a.setup)
	if got := a.setup.modelID(); got != "llama3.2:3b" {
		t.Errorf("default model = %q, want the first installed one (qwen3:8b is not pulled)", got)
	}
	h.models = []config.ModelChoice{{ID: "gemma3:4b"}, {ID: "qwen3:8b"}}
	a.open(pageSetup)
	a.loadModels(a.setup)
	if got := a.setup.modelID(); got != "qwen3:8b" {
		t.Errorf("default model = %q, want the preset when it is installed", got)
	}
}

func TestSetupOffersModelsThenEffort(t *testing.T) {
	h := &fakeHost{t: t, providers: []config.Provider{{ID: "claude-cli", Backend: "claude-cli", Model: "sonnet", Efforts: []string{"low", "high"},
		Models: []config.ModelChoice{{ID: "sonnet", Label: "Sonnet"}, {ID: "haiku", Label: "Haiku", NoEffort: true}}}}}
	a := newApp(h, Start{Page: pageMenu})
	a.open(pageSetup)
	d := a.setup
	a.loadModels(d)
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
	a := newApp(h, Start{Page: pageLive, NeedSetup: true, Intake: pipeline.Intake{Idea: "x"}})
	a.Init()
	a.loadModels(a.setup)
	a.setup.model, a.setup.custom = otherModel, " qwen3:8b "

	cmd := a.finishSetup()
	if a.page != pageDownload || len(h.saved) != 0 {
		t.Fatalf("a model that is not here must be downloaded before it is saved: page=%v saved=%v", a.page, h.saved)
	}
	_, cmd = a.Update(cmd()) // pulling manifest
	_, cmd = a.Update(cmd()) // a quarter done
	if view := a.View(); !strings.Contains(view, "Downloading qwen3:8b") || !strings.Contains(view, "500 MB of 2.0 GB  25%") {
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
	a.loadModels(a.setup)
	a.setup.model, a.setup.custom = otherModel, "nope:1b"
	pump(t, a, a.finishSetup(), pageDownload)
	if a.page != pageSetup || len(h.saved) != 0 || !strings.Contains(a.note, "no model called nope:1b") || a.banner != "" {
		t.Errorf("page=%v saved=%v note=%q banner=%q", a.page, h.saved, a.note, a.banner)
	}

	h.missing, h.pullErr, h.hang = "big:70b", nil, true
	a.setup.model, a.setup.custom = otherModel, "big:70b"
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
	a.loadModels(a.setup)
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
	d := &setupDraft{custom: "typed-so-far"}
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
		d := &setupDraft{custom: id}
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
