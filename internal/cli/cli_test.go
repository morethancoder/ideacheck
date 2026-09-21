package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/schema"
)

type run struct {
	code           int
	stdout, stderr string
}

// runCLI drives the real command tree with a hermetic environment: no user config
// dir, no env vars, stdin as given.
func runCLI(t *testing.T, stdin string, args ...string) run {
	t.Helper()
	return execWithStore(t, filepath.Join(t.TempDir(), "history.db"), stdin, args...)
}

func execWithStore(t *testing.T, storePath, stdin string, args ...string) run {
	t.Helper()
	var out, errb bytes.Buffer
	a := &app{
		stdout: &out, stderr: &errb,
		environ: func() []string { return []string{"IDEACHECK_STORE__PATH=" + storePath} },
		getenv:  func(string) string { return "" },
		home:    t.TempDir(),
		input:   osInputEnv(),
	}
	a.input.Stdin = strings.NewReader(stdin)
	a.input.StdinPiped = stdin != ""
	code := a.run(context.Background(), args)
	return run{code, out.String(), errb.String()}
}

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", filepath.FromSlash(schema.Path)))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("result.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("result.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The spec's end-to-end test: `ideacheck "..." -o json -b mock` validates
// against the published schema, for both result shapes.
func TestEndToEndOutputMatchesPublishedSchema(t *testing.T) {
	sch := compileSchema(t)
	fixtures := t.TempDir()
	if err := os.WriteFile(filepath.Join(fixtures, "has_why_now.json"), []byte(`{"noul":0.2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgDir := t.TempDir()
	cfg := "backends:\n  mock:\n    fixtures_dir: " + fixtures + "\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, status string
		code         int
		args         []string
	}{
		{"verdict", "ok", 0, []string{"A CLI that scores ideas", "-o", "json", "-b", "mock"}},
		{"gaps scored anyway", "ok", 0, []string{"A CLI that scores ideas", "-o", "json", "-b", "mock", "-c", cfgDir}},
		{"gaps under --strict", "needs_input", 2, []string{"A CLI that scores ideas", "-o", "json", "-b", "mock", "-c", cfgDir, "--strict"}},
	}
	for _, c := range cases {
		r := runCLI(t, "", c.args...)
		if r.code != c.code {
			t.Fatalf("%s: exit %d (want %d): %s", c.name, r.code, c.code, r.stderr)
		}
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(r.stdout))
		if err != nil {
			t.Fatalf("%s: stdout is not pure JSON: %v\n%s", c.name, err, r.stdout)
		}
		if err := sch.Validate(doc); err != nil {
			t.Errorf("%s: output violates the schema: %v", c.name, err)
		}
		var got struct{ Status, Verdict string }
		_ = json.Unmarshal([]byte(r.stdout), &got)
		if got.Status != c.status || (c.status == "ok") != (got.Verdict != "") {
			t.Errorf("%s: status=%q verdict=%q", c.name, got.Status, got.Verdict)
		}
	}
}

func TestArgumentResolution(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "idea.md")
	if err := os.WriteFile(file, []byte("from file"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := inputEnv{
		Stdin:    strings.NewReader("from stdin"),
		ReadFile: os.ReadFile,
		IsFile:   func(p string) bool { return p == file },
	}
	piped := env
	piped.StdinPiped = true
	cases := []struct {
		name        string
		args        []string
		fileFlag    string
		env         inputEnv
		want        string
		interactive bool
	}{
		{"dash reads stdin", []string{"-"}, "", env, "from stdin", false},
		{"existing file", []string{file}, "", env, "from file", false},
		{"file flag", nil, file, env, "from file", false},
		{"piped stdin, no args", nil, "", piped, "from stdin", false},
		{"text beats piped stdin", []string{"my", "idea"}, "", piped, "my idea", false},
		{"args are joined", []string{"an", "app", "that"}, "", env, "an app that", false},
		{"a non-file word is text", []string{"serve"}, "", env, "serve", false},
		{"nothing on a terminal opens the form", nil, "", env, "", true},
	}
	for _, c := range cases {
		c.env.Stdin = strings.NewReader("from stdin")
		got, err := resolveInput(c.args, c.fileFlag, c.env)
		if err != nil || string(got.Data) != c.want || got.Interactive != c.interactive {
			t.Errorf("%s: got %q interactive=%v err=%v", c.name, got.Data, got.Interactive, err)
		}
	}
	if _, err := resolveInput(nil, filepath.Join(dir, "absent.md"), env); err == nil {
		t.Error("a missing -f file must be an error, not idea text")
	}
}

func TestOutputFormat(t *testing.T) {
	cases := []struct {
		flag     string
		json     bool
		tty      bool
		want     string
		wantFail bool
	}{
		{"", false, true, "pretty", false},
		{"", false, false, "json", false}, // not a TTY → JSON without being asked
		{"plain", false, false, "plain", false},
		{"pretty", true, true, "json", false},
		{"json", false, true, "json", false},
		{"xml", false, true, "", true},
	}
	for _, c := range cases {
		got, err := outputFormat(c.flag, c.json, c.tty)
		if got != c.want || (err != nil) != c.wantFail {
			t.Errorf("outputFormat(%q,%v,%v) = %q, %v", c.flag, c.json, c.tty, got, err)
		}
	}
}

func TestSubcommandNamesAndTheDoubleDashEscape(t *testing.T) {
	if r := runCLI(t, "", "serve", "-b", "claude-cli"); r.code != 1 || !strings.Contains(r.stderr, "--allow-cli-backend") {
		t.Errorf("`serve` must be the subcommand and refuse claude-cli by default: %+v", r)
	}
	if r := runCLI(t, "", "benchmark"); r.code != 1 || !strings.Contains(r.stderr, "--dry-run") {
		t.Errorf("`benchmark` must alias bench: %+v", r)
	}
	if r := runCLI(t, "", "past"); r.code != 0 || !strings.Contains(r.stdout, "[]") {
		t.Errorf("`past` must alias history: %+v", r)
	}
	r := runCLI(t, "", "-b", "mock", "-o", "json", "--", "serve")
	if r.code != 0 || !strings.Contains(r.stdout, `"status": "ok"`) {
		t.Errorf("`-- serve` must check the idea \"serve\": %+v", r)
	}
}

func TestStdinIntakeAndFlagsReachThePipeline(t *testing.T) {
	r := runCLI(t, `{"idea":"a weekend tool","profile":{"skills":"go"}}`, "-b", "mock", "-r", "side_project", "-o", "json")
	var got struct {
		Rubric struct{ Name string }
		Model  string
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || got.Rubric.Name != "side_project" {
		t.Fatalf("rubric = %q err=%v stderr=%s", got.Rubric.Name, err, r.stderr)
	}
	if r := runCLI(t, "", "x", "-b", "mock", "-r", "nope"); r.code != 1 || !strings.Contains(r.stderr, "nope") {
		t.Errorf("unknown rubric: %+v", r)
	}
	if r := runCLI(t, "", "x", "-b", "gpt"); r.code != 1 || !strings.Contains(r.stderr, "gpt") {
		t.Errorf("unknown backend: %+v", r)
	}
	if r := runCLI(t, `{"idea":"x","fields":{"pitch":"y"}}`, "-b", "mock"); r.code != 1 || !strings.Contains(r.stderr, "pitch") {
		t.Errorf("unknown intake field: %+v", r)
	}
}

func TestSavedProfileFillsOnlyWhatIntakeLeftEmpty(t *testing.T) {
	a := &app{getenv: func(string) string { return "" }, home: t.TempDir(), input: osInputEnv()}
	dir := filepath.Join(a.home, ".config", "ideacheck")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte("skills: go\ndomains: payroll\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, err := ParseForTest(`{"idea":"x","profile":{"skills":"rust"}}`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.withProfile(in, "")
	if err != nil || got.Profile["skills"] != "rust" || got.Profile["domains"] != "payroll" {
		t.Errorf("profile = %v err=%v", got.Profile, err)
	}
	if _, err := a.withProfile(in, "missing-name"); err == nil {
		t.Error("an explicitly requested profile that does not exist must be an error")
	}
}

func TestConfigDumpNeverOverwritesEdits(t *testing.T) {
	dir := t.TempDir()
	first := runCLI(t, "", "config", "dump", dir)
	wrote := strings.Count(first.stdout, "wrote")
	if first.code != 0 || wrote == 0 {
		t.Fatalf("first dump: %+v", first)
	}
	edited := filepath.Join(dir, "rubrics", "business.yaml")
	if err := os.WriteFile(edited, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := runCLI(t, "", "config", "dump", dir); strings.Count(r.stdout, "kept") != wrote {
		t.Fatalf("second dump: %+v", r)
	}
	if b, _ := os.ReadFile(edited); string(b) != "mine" {
		t.Error("dump overwrote an edited file")
	}
}

// `config dump` with no directory prints. It used to write, and writing into the
// config directory ideacheck reads leaves copies that shadow later upgrades.
func TestConfigDumpPrintsAndRefusesTheLiveDir(t *testing.T) {
	dir := t.TempDir()
	printed := runCLI(t, "", "-c", dir, "config", "dump")
	if printed.code != 0 || !strings.Contains(printed.stdout, "# ==> rubrics/_gaps.yaml <==") {
		t.Fatalf("dump did not print the effective files: %+v", printed)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("dump wrote %d file(s) into the config dir", len(entries))
	}
	if r := runCLI(t, "", "-c", dir, "config", "dump", dir); r.code == 0 || !strings.Contains(r.stderr, "--force") {
		t.Errorf("writing into the live config dir must be refused: %+v", r)
	}
	if r := runCLI(t, "", "-c", dir, "config", "dump", dir, "--force"); r.code != 0 || !strings.Contains(r.stdout, "wrote") {
		t.Errorf("--force must allow it: %+v", r)
	}
}

// An agent has no terminal to answer in, so every fact has a flag, and the
// status is in the exit code.
func TestAgentModeTakesFactsAsFlags(t *testing.T) {
	r := runCLI(t, "", "A CLI that scores ideas", "-b", "mock", "--agent",
		"--answer", "why_now=the models got cheap", "--answer", "profile.background=ten years in payroll",
		"--context", "a working prototype exists")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	var got struct {
		Status  string
		Missing []struct{ Fills string }
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, r.stdout)
	}
	if got.Status != "ok" {
		t.Errorf("status = %q", got.Status)
	}
	for _, m := range got.Missing {
		if m.Fills == "why_now" || m.Fills == "profile.background" {
			t.Errorf("%s was given on the command line but reported missing", m.Fills)
		}
	}
	if r := runCLI(t, "", "x", "-b", "mock", "--agent", "--answer", "why_now"); r.code != 1 || !strings.Contains(r.stderr, "field=value") {
		t.Errorf("a malformed --answer must fail loudly: %+v", r)
	}
	if r := runCLI(t, "", "x", "-b", "mock", "--agent", "--answer", "pitch=x"); r.code != 1 || !strings.Contains(r.stderr, "not an intake field") {
		t.Errorf("an unknown field must fail loudly: %+v", r)
	}
}

// A fact in the document's frontmatter is a fact given, the same as --answer.
func TestFrontmatterSuppliesFacts(t *testing.T) {
	doc := "---\ncontext: a prototype has been running for a year\nfields: {why_now: the models got cheap}\n---\nA CLI that scores ideas\n"
	r := runCLI(t, doc, "-b", "mock", "--agent")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	var got struct {
		Missing []struct{ Fills string }
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, r.stdout)
	}
	for _, m := range got.Missing {
		if m.Fills == "why_now" {
			t.Error("why_now was in the frontmatter but reported missing")
		}
	}
	if r := runCLI(t, "---\nnope: 1\n---\nan idea\n", "-b", "mock", "--agent"); r.code != 1 || !strings.Contains(r.stderr, "frontmatter") {
		t.Errorf("an unknown frontmatter key must fail loudly: %+v", r)
	}
}

func TestConfigSetKeepsAChoiceAndUndoesABadOne(t *testing.T) {
	dir := t.TempDir()
	if r := runCLI(t, "", "-c", dir, "config", "set", "backends.mock.seed", "7"); r.code != 0 {
		t.Fatalf("set: %+v", r)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil || !strings.Contains(string(b), "seed: 7") {
		t.Fatalf("config.yaml = %q err=%v", b, err)
	}
	if r := runCLI(t, "", "-c", dir, "config", "set", "timeouts.question", "[1,2]"); r.code == 0 {
		t.Errorf("a value that does not load must fail: %+v", r)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "config.yaml")); string(after) != string(b) {
		t.Errorf("a failed set must leave config.yaml as it was:\n%s", after)
	}
}

func TestChecksArePersistedAndCanBeReshown(t *testing.T) {
	db := filepath.Join(t.TempDir(), "history.db")
	first := execWithStore(t, db, "", "A payroll compliance tool", "-b", "mock", "-o", "json")
	execWithStore(t, db, "", "A second idea", "-b", "mock", "-o", "json")
	if first.code != 0 {
		t.Fatal(first.stderr)
	}
	var want, got struct{ ID, Verdict string }
	_ = json.Unmarshal([]byte(first.stdout), &want)

	shown := execWithStore(t, db, "", "show", "1", "-o", "json")
	_ = json.Unmarshal([]byte(shown.stdout), &got)
	if shown.code != 0 || got != want {
		t.Errorf("show 1 = %+v, want %+v (%s)", got, want, shown.stderr)
	}
	last := execWithStore(t, db, "", "last", "-o", "json")
	_ = json.Unmarshal([]byte(last.stdout), &got)
	if got.ID == want.ID || got.ID == "" {
		t.Errorf("last must be the second check, got %+v", got)
	}
	hist := execWithStore(t, db, "", "history", "-n", "1")
	var rows []struct {
		Seq  int
		Idea string
	}
	if err := json.Unmarshal([]byte(hist.stdout), &rows); err != nil || len(rows) != 1 || rows[0].Seq != 2 || rows[0].Idea != "A second idea" {
		t.Errorf("history -n 1 = %s (%v)", hist.stdout, err)
	}
	if r := execWithStore(t, db, "", "show", "42"); r.code != 1 || !strings.Contains(r.stderr, "42") {
		t.Errorf("show 42: %+v", r)
	}
}

func TestFirstRunDetectionAndSettingsTakeEffectImmediately(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{}
	a := &app{getenv: func(k string) string { return env[k] }, environ: func() []string { return nil }, home: home, input: osInputEnv()}
	flags := &checkFlags{}
	if !a.needsSetup(flags) {
		t.Fatal("a fresh machine needs setup")
	}
	if a.needsSetup(&checkFlags{backend: "mock"}) {
		t.Error("-b is an explicit choice: no setup prompt")
	}
	env["IDEACHECK_BACKEND"] = "mock"
	if a.needsSetup(flags) {
		t.Error("IDEACHECK_BACKEND is an explicit choice: no setup prompt")
	}
	delete(env, "IDEACHECK_BACKEND")

	h := &host{app: a, flags: flags, ctx: context.Background()}
	if backend, _, _ := h.Current(); backend != "not set up" {
		t.Errorf("Current before setup = %q", backend)
	}
	var openai config.Provider
	for _, p := range h.Providers() {
		if p.ID == "openai" {
			openai = p
		}
	}
	if err := h.SaveSetup(openai, "gpt-y", "", "sk-test"); err != nil {
		t.Fatal(err)
	}
	if a.needsSetup(flags) || !h.Key("OPENAI_API_KEY").Found() {
		t.Error("setup must be remembered and the key retrievable")
	}
	if backend, model, _ := h.Current(); backend != "structured" || model != "gpt-y" {
		t.Errorf("Current after setup = %q %q", backend, model)
	}
	engine, err := h.Engine() // no restart: the next engine uses the new settings
	if err != nil || engine.Config.Active().Provider != "openai" || engine.Config.Active().BaseURL != "https://api.openai.com/v1" {
		t.Errorf("engine after setup: %v %+v", err, engine)
	}
	cfgFile, _ := os.ReadFile(filepath.Join(home, ".config", "ideacheck", "config.yaml"))
	if strings.Contains(string(cfgFile), "sk-test") {
		t.Error("the API key leaked into config.yaml")
	}
}

func TestAgentModeWithoutSetupUsesLocalOllamaOrSaysWhatToDo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("no question may be sent before the model is known to exist: %s", r.URL.Path)
		}
		io.WriteString(w, tags)
	}))
	defer srv.Close()
	var out, errb bytes.Buffer
	a := &app{stdout: &out, stderr: &errb, getenv: func(string) string { return "" }, home: t.TempDir(), input: osInputEnv(),
		environ: func() []string { return []string{"IDEACHECK_BACKENDS__LOGPROB__BASE_URL=" + srv.URL + "/v1"} }}
	a.input.Stdin = strings.NewReader("")
	code := a.run(context.Background(), []string{"an idea", "-o", "json"})
	msg := errb.String()
	if code != 1 || !strings.Contains(msg, "ollama pull qwen3:8b") || !strings.Contains(msg, "llama3.2:3b") || !strings.Contains(msg, "ideacheck setup") {
		t.Errorf("unconfigured agent mode: code=%d stderr=%q", code, msg)
	}
}

// The profile decides whether founder-fit is scored at all, so it must be
// settable without a terminal — the caller who needs it most has none.
func TestProfileCanBeSetWithoutATerminal(t *testing.T) {
	home := t.TempDir()
	set := func(args ...string) run {
		var out, errb bytes.Buffer
		a := &app{stdout: &out, stderr: &errb, home: home,
			environ: func() []string { return nil }, getenv: func(string) string { return "" }, input: osInputEnv()}
		return run{a.run(context.Background(), args), out.String(), errb.String()}
	}
	if r := set("profile", "show"); r.code != 0 || !strings.Contains(r.stdout, "no profile saved") {
		t.Fatalf("show with none saved: %+v", r)
	}
	if r := set("profile", "set", "background=ten years in payroll", "skills=go"); r.code != 0 ||
		!strings.Contains(r.stdout, "ten years in payroll") || !strings.Contains(r.stdout, "go") {
		t.Fatalf("set: %+v", r)
	}
	if r := set("profile", "set", "skills=go and svelte"); r.code != 0 || !strings.Contains(r.stdout, "ten years in payroll") {
		t.Errorf("set must merge, not replace: %+v", r)
	}
	if r := set("profile", "set", "skills="); r.code != 0 || strings.Contains(r.stdout, "svelte") {
		t.Errorf("an empty value must clear the field: %+v", r)
	}
	if r := set("profile", "set", "nope=x"); r.code == 0 || !strings.Contains(r.stderr, "not a profile field") {
		t.Errorf("an unknown field must fail loudly: %+v", r)
	}
	saved, err := os.ReadFile(filepath.Join(home, ".config", "ideacheck", "profile.yaml"))
	if err != nil || !strings.Contains(string(saved), "background: ten years in payroll") {
		t.Errorf("profile.yaml = %q err=%v", saved, err)
	}
}

// `search up` on a machine without Docker: the failure is the install guide.
func TestSearchUpWithoutDockerSaysHowToInstallIt(t *testing.T) {
	var out, errb bytes.Buffer
	a := &app{
		stdout: &out, stderr: &errb,
		environ: func() []string { return nil },
		getenv:  func(string) string { return "" },
		home:    t.TempDir(),
		docker: func(context.Context, []string, io.Reader, ...string) (string, error) {
			return "", &exec.Error{Name: "docker", Err: exec.ErrNotFound}
		},
	}
	code := a.run(context.Background(), []string{"search", "up"})
	if said := errb.String(); code != 1 || !strings.Contains(said, "Docker is not installed") || !strings.Contains(said, "docs.docker.com") {
		t.Errorf("code %d, stderr:\n%s", code, said)
	}
}
