package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"ghpm/internal/app"
)

// progressPrinter renders concurrent install/check events. On a terminal it
// keeps one live status line; otherwise it prints only completed items.
type progressPrinter struct {
	mu      sync.Mutex
	tty     bool
	active  map[string]string
	stop    chan struct{}
	done    chan struct{}
	started bool
}

func newProgressPrinter() *progressPrinter {
	p := &progressPrinter{
		tty:    isTTY(os.Stderr),
		active: map[string]string{},
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	if p.tty {
		p.started = true
		go p.loop()
	}
	return p
}

func (p *progressPrinter) loop() {
	defer close(p.done)
	t := time.NewTicker(150 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			p.clearLine()
			return
		case <-t.C:
			p.render()
		}
	}
}

func (p *progressPrinter) render() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.active) == 0 {
		return
	}
	ids := make([]string, 0, len(p.active))
	for id := range p.active {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var parts []string
	for _, id := range ids {
		parts = append(parts, id+" "+p.active[id])
	}
	fmt.Fprintf(os.Stderr, "\r\033[K%s", strings.Join(parts, "  "))
}

func (p *progressPrinter) clearLine() {
	if p.tty {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
}

// Event implements app.ProgressFunc.
func (p *progressPrinter) Event(ev app.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch ev.Kind {
	case "resolve":
		p.active[ev.ID] = "resolving…"
	case "install":
		p.active[ev.ID] = "installing…"
	case "download":
		d := ev.Download
		if d.Done {
			p.active[ev.ID] = "verifying…"
			break
		}
		if d.Total > 0 {
			p.active[ev.ID] = fmt.Sprintf("%3.0f%% %s/s", d.Percent(), humanBytes(d.BytesPerSec))
		} else {
			p.active[ev.ID] = fmt.Sprintf("%s %s/s", humanBytes(float64(d.Downloaded)), humanBytes(d.BytesPerSec))
		}
	case "done":
		delete(p.active, ev.ID)
		if p.tty {
			fmt.Fprint(os.Stderr, "\r\033[K")
		}
		fmt.Fprintf(os.Stderr, "✓ %s\n", ev.Message)
	case "error":
		delete(p.active, ev.ID)
		if p.tty {
			fmt.Fprint(os.Stderr, "\r\033[K")
		}
		fmt.Fprintf(os.Stderr, "✗ %s: %v\n", ev.ID, ev.Err)
	case "check":
		if strings.HasPrefix(ev.Message, "checking ") || ev.Message == "" {
			return
		}
		if ev.Err != nil {
			fmt.Fprintf(os.Stderr, "✗ %s: %v\n", ev.ID, ev.Err)
			return
		}
		fmt.Fprintf(os.Stderr, "✓ %s %s\n", ev.ID, ev.Message)
	}
}

// Stop clears the live line and waits for the renderer.
func (p *progressPrinter) Stop() {
	if !p.started {
		return
	}
	close(p.stop)
	<-p.done
	p.started = false
}
