package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/morethancoder/ideacheck/ideacheck"
	"github.com/morethancoder/ideacheck/judge"
)

type row struct {
	q      judge.Question
	stage  string
	answer *judge.Answer
	value  *float64
}

type outcome struct {
	res *ideacheck.Result
	err error
}

type (
	eventMsg  ideacheck.Event
	closedMsg struct{}
)

type liveModel struct {
	header string
	rows   []row
	index  map[string]int
	spin   spinner.Model
	events <-chan ideacheck.Event
}

func newLive(header string, events <-chan ideacheck.Event) liveModel {
	return liveModel{
		header: header, index: map[string]int{}, events: events,
		spin: spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(accent)),
	}
}

func (m liveModel) Init() tea.Cmd { return tea.Batch(m.spin.Tick, m.wait()) }

// wait blocks for the next pipeline event; a closed channel means the check is over.
func (m liveModel) wait() tea.Cmd {
	return func() tea.Msg {
		e, ok := <-m.events
		if !ok {
			return closedMsg{}
		}
		return eventMsg(e)
	}
}

func (m liveModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case eventMsg:
		return m.apply(ideacheck.Event(msg)), m.wait()
	case closedMsg:
		return m, tea.Quit
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}
	return m, nil
}

// apply adds a row when a question starts and fills it in when the answer lands.
func (m liveModel) apply(e ideacheck.Event) liveModel {
	i, ok := m.index[e.Question.ID]
	if !ok {
		i = len(m.rows)
		m.index[e.Question.ID] = i
		m.rows = append(m.rows, row{q: e.Question, stage: e.Stage})
	}
	if e.Answer != nil {
		m.rows[i].answer, m.rows[i].value = e.Answer, e.Value
	}
	return m
}

func (m liveModel) View() string { return m.body() }

// body is the question list; the app supplies the surrounding chrome.
func (m liveModel) body() string {
	var b strings.Builder
	if len(m.rows) == 0 {
		return m.spin.View() + dim.Render(" starting…")
	}
	stage := ""
	for _, r := range m.rows {
		if r.stage != stage {
			stage = r.stage
			b.WriteString("\n" + heading.Render(stageTitle(stage)) + "\n")
		}
		b.WriteString(m.renderRow(r) + "\n")
	}
	return b.String()
}

func stageTitle(stage string) string {
	switch stage {
	case ideacheck.StagePreflight:
		return "What is stated · idea type"
	case ideacheck.StageExtract:
		return "Reading the description"
	case ideacheck.StageResearch:
		return "Looking it up on the web"
	case ideacheck.StageExplain:
		return "Summary"
	}
	return "Scoring"
}

func (m liveModel) renderRow(r row) string {
	name := fmt.Sprintf("%-28s", clip(r.q.ID, 28))
	switch {
	case r.answer == nil:
		return fmt.Sprintf("  %s %s", m.spin.View(), dim.Render(name))
	case r.answer.Failed():
		return fmt.Sprintf("  %s %s %s", bad.Render("✗"), name, bad.Render(r.answer.Err))
	case r.stage == ideacheck.StageExplain:
		return fmt.Sprintf("  %s %s %s", good.Render("✓"), name, dim.Render("written"))
	case r.stage == ideacheck.StageExtract:
		return fmt.Sprintf("  %s %s %s", good.Render("✓"), name, dim.Render("read"))
	case r.value == nil && r.answer.Confidence == 0: // a step of the web lookup: a count, not a judgment
		return fmt.Sprintf("  %s %s %s", good.Render("✓"), name, dim.Render(r.answer.Choice))
	case r.value == nil:
		return fmt.Sprintf("  %s %s %s  %s", good.Render("✓"), name, accent.Render(r.answer.Choice), dim.Render(pct(r.answer.Confidence)))
	}
	return fmt.Sprintf("  %s %s %s %s  %s  %s", good.Render("✓"), name, ColorBar(*r.value, r.q.Polarity),
		scoreStyle(*r.value, r.q.Polarity).Render(fmt.Sprintf("%.2f", *r.value)), dim.Render(pct(r.answer.Confidence)), dim.Render(Arrow(r.q.Polarity)))
}

// clip shortens a row name to the column: findings are named by their title.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func pct(v float64) string { return fmt.Sprintf("%3.0f%%", v*100) }
