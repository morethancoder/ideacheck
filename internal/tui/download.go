package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/ui"
)

// Progress is one update from a model download.
type Progress struct {
	Status      string // the server's own words: "pulling manifest", "verifying sha256 digest"…
	Done, Total int64  // bytes, summed over every part seen so far
}

type (
	progressMsg   Progress
	downloadedMsg struct{ err error }
)

// download is a model being fetched before setup saves it: a model that is
// not on this machine could never answer, so it is downloaded first.
type download struct {
	model  string
	last   Progress
	events chan Progress
	done   chan error
	ctx    context.Context
	cancel context.CancelFunc
}

func (a *App) openDownload(p config.Provider, model string) tea.Cmd {
	ctx, cancel := context.WithCancel(a.ctx)
	d := &download{model: model, events: make(chan Progress, 16), done: make(chan error, 1), ctx: ctx, cancel: cancel}
	a.page, a.dl, a.banner, a.note = pageDownload, d, "", ""
	go func() {
		err := a.host.Download(ctx, p, model, func(p Progress) {
			select {
			case d.events <- p:
			case <-ctx.Done():
			}
		})
		close(d.events)
		d.done <- err
	}()
	return d.wait()
}

// wait blocks for the next update; a closed channel means the download is over.
func (d *download) wait() tea.Cmd {
	return func() tea.Msg {
		if p, ok := <-d.events; ok {
			return progressMsg(p)
		}
		return downloadedMsg{<-d.done}
	}
}

func (a *App) updateDownload(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case progressMsg:
		a.dl.last = Progress(msg)
		return a.dl.wait()
	case downloadedMsg:
		d := a.dl
		stopped := d.ctx.Err() != nil // esc, checked before the cleanup below cancels it anyway
		d.cancel()
		a.dl = nil
		if msg.err == nil {
			return a.saveSetup()
		}
		note := fmt.Sprintf("Could not download %s: %v", d.model, msg.err)
		if stopped {
			note = "Download stopped. Choose " + d.model + " again to pick up where it left off, or pick another model."
		}
		cmd := a.open(pageSetup)
		a.note = note
		return cmd
	case tea.KeyMsg:
		if msg.String() == "esc" {
			a.dl.cancel() // the download returns, and downloadedMsg goes back to setup
		}
	}
	return nil
}

// view draws the download the way the installer draws its own: the same bar,
// the same percentage, the same "so far / in total" on the right.
func (d *download) view(width int) string {
	head := bold.Render("Downloading "+d.model) + dim.Render(" — once; it stays on this machine and runs free") + "\n\n"
	p := d.last
	status := p.Status
	if strings.HasPrefix(status, "pulling ") && status != "pulling manifest" {
		status = "" // "pulling <digest>" is what the bar already shows
	}
	if p.Total == 0 {
		return head + dim.Render(orStarting(status))
	}
	frac := float64(p.Done) / float64(p.Total)
	filled, rest := ui.Bar(frac, min(max(width-34, 10), 40))
	line := fmt.Sprintf("  %s %3.0f%%  %s", good.Render(filled)+dim.Render(rest), frac*100,
		dim.Render(ui.Bytes(p.Done)+" / "+ui.Bytes(p.Total)))
	if status != "" {
		line += dim.Render("  · " + status)
	}
	return head + line
}

func orStarting(status string) string {
	if status == "" {
		return "starting…"
	}
	return status + "…"
}
