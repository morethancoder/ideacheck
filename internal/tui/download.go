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

// download is something setup waits for before it moves on: a model being
// fetched (one that is not on this machine could never answer), or the search
// engine being started. The page is the same; what runs and what follows differ.
type download struct {
	title, about string // the heading, and the dim half of it
	then         func(err error, stopped bool) tea.Cmd
	last         Progress
	events       chan Progress
	done         chan error
	ctx          context.Context
	cancel       context.CancelFunc
}

// openDownload fetches a model, then carries on with setup (which may have
// another to fetch); a failure goes back to the wizard, because a model that is
// not here cannot be saved as the choice.
func (a *App) openDownload(p config.Provider, model string) tea.Cmd {
	return a.await("Downloading "+model, "once; it stays on this machine and runs free",
		func(ctx context.Context, progress func(Progress)) error {
			return a.host.Download(ctx, p, model, progress)
		},
		func(err error, stopped bool) tea.Cmd {
			if err == nil {
				a.setup.fetched[model] = true
				return a.finishSetup()
			}
			note := fmt.Sprintf("Could not download %s: %v", model, err)
			if stopped {
				note = "Download stopped. Choose " + model + " again to pick up where it left off, or pick another model."
			}
			cmd := a.open(pageSetup)
			a.note = note
			return cmd
		})
}

// await opens the progress page over run, and hands its outcome to then.
func (a *App) await(title, about string, run func(context.Context, func(Progress)) error, then func(error, bool) tea.Cmd) tea.Cmd {
	ctx, cancel := context.WithCancel(a.ctx)
	d := &download{title: title, about: about, then: then, events: make(chan Progress, 16), done: make(chan error, 1), ctx: ctx, cancel: cancel}
	a.page, a.dl, a.banner, a.note = pageDownload, d, "", ""
	go func() {
		err := run(ctx, func(p Progress) {
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
		return d.then(msg.err, stopped)
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
	head := heading.Render(d.title) + dim.Render(" — "+d.about) + "\n\n"
	p := d.last
	status := p.Status
	if strings.HasPrefix(status, "pulling ") && status != "pulling manifest" {
		status = "" // "pulling <digest>" is what the bar already shows
	}
	if p.Total == 0 {
		return head + "  " + accent.Render("◌ ") + dim.Render(orStarting(status))
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
