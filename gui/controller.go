package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"ghpm/internal/app"
)

// opProgress is one row in the progress panel.
type opProgress struct {
	ID      string
	Phase   string
	Message string
	Err     string
	Percent float64
	Speed   float64
}

// controller holds everything the window shows. The view and menu callbacks
// run on the main thread; long operations run in a goroutine and come back
// through update.
type controller struct {
	be    backend
	win   *mygo.Window
	async func(func())

	views    []app.AppView
	rows     []int // indices into views after filtering
	query    string
	selected int
	table    ui.ListState

	status    string
	statusErr bool
	busy      bool

	progress map[string]*opProgress
	order    []string

	showAdd       bool
	addRepo       string
	addID         string
	addInstall    bool
	addPrerelease bool
	addErr        string

	showRemove      bool
	removeID        string
	removeInstalled bool

	showDetails bool
}

func newController(be backend) *controller {
	return &controller{
		be:       be,
		async:    func(fn func()) { go fn() },
		progress: map[string]*opProgress{},
	}
}

// ---- state ----

func (c *controller) invalidate() {
	if c.win != nil {
		c.win.Invalidate()
	}
}

// update runs fn on the main thread and draws a frame afterwards.
func (c *controller) update(fn func()) {
	if c.win != nil {
		c.win.Update(fn)
		return
	}
	fn()
}

func (c *controller) setViews(views []app.AppView) {
	c.views = views
	c.applyFilter()
	c.invalidate()
}

func (c *controller) applyFilter() {
	keep := c.currentID()
	q := strings.ToLower(strings.TrimSpace(c.query))
	rows := c.rows[:0]
	for i := range c.views {
		v := &c.views[i]
		if q != "" && !strings.Contains(strings.ToLower(v.ID+" "+v.Repo+" "+v.Description), q) {
			continue
		}
		rows = append(rows, i)
	}
	c.rows = rows
	if keep != "" {
		for i, idx := range c.rows {
			if c.views[idx].ID == keep {
				c.selected = i
				return
			}
		}
	}
	if c.selected >= len(c.rows) {
		c.selected = len(c.rows) - 1
	}
	if c.selected < 0 {
		c.selected = 0
	}
}

func (c *controller) currentID() string {
	if c.selected < 0 || c.selected >= len(c.rows) {
		return ""
	}
	idx := c.rows[c.selected]
	if idx < 0 || idx >= len(c.views) {
		return ""
	}
	return c.views[idx].ID
}

func (c *controller) selectedView() *app.AppView {
	if c.selected < 0 || c.selected >= len(c.rows) {
		return nil
	}
	idx := c.rows[c.selected]
	if idx < 0 || idx >= len(c.views) {
		return nil
	}
	return &c.views[idx]
}

func (c *controller) refresh() {
	views, err := c.be.Views()
	if err != nil {
		c.status = err.Error()
		c.statusErr = true
		c.invalidate()
		return
	}
	c.setViews(views)
}

// ---- operations ----

func (c *controller) startJob(label string, fn func(context.Context, app.ProgressFunc) (string, error)) {
	if c.busy {
		c.status = "Another operation is still running"
		c.statusErr = true
		c.invalidate()
		return
	}
	c.busy = true
	c.status = label + "…"
	c.statusErr = false
	c.progress = map[string]*opProgress{}
	c.order = nil
	c.invalidate()

	async := c.async
	if async == nil {
		async = func(fn func()) { go fn() }
	}
	async(func() {
		ctx := context.Background()
		msg, err := fn(ctx, func(ev app.Event) {
			c.update(func() { c.applyEvent(ev) })
		})
		views, viewErr := c.be.Views()
		c.update(func() {
			c.busy = false
			if err != nil {
				if msg != "" {
					c.status = msg + ": " + err.Error()
				} else {
					c.status = err.Error()
				}
				c.statusErr = true
			} else {
				c.status = msg
				c.statusErr = false
			}
			if viewErr == nil {
				c.views = views
				c.applyFilter()
			}
			c.invalidate()
		})
	})
}

func (c *controller) applyEvent(ev app.Event) {
	key := ev.ID
	if key == "" {
		key = "ghpm"
	}
	p := c.progress[key]
	if p == nil {
		p = &opProgress{ID: key}
		c.progress[key] = p
		c.order = append(c.order, key)
	}
	if ev.Download.Total > 0 {
		p.Percent = ev.Download.Percent()
		p.Speed = ev.Download.BytesPerSec
	}
	switch ev.Kind {
	case "resolve":
		p.Phase = "resolving"
		p.Message = ev.Message
	case "install":
		p.Phase = "installing"
		p.Message = ev.Message
	case "download":
		p.Phase = "downloading"
		if ev.Download.Done {
			p.Phase = "verifying"
		}
	case "done":
		p.Phase = "done"
		p.Percent = 100
		p.Message = ev.Message
		p.Err = ""
	case "error":
		p.Phase = "error"
		if ev.Err != nil {
			p.Err = ev.Err.Error()
		} else {
			p.Err = "failed"
		}
	case "check":
		switch {
		case ev.Err != nil:
			p.Phase = "error"
			p.Err = ev.Err.Error()
		case ev.Message != "" && !strings.HasPrefix(ev.Message, "checking "):
			p.Phase = "checked"
			p.Message = ev.Message
		default:
			p.Phase = "checking"
		}
	}
}

func (c *controller) checkAll(notify bool) {
	c.startJob("Checking for updates", func(ctx context.Context, pf app.ProgressFunc) (string, error) {
		results := c.be.CheckFresh(ctx, nil, pf)
		updates, failures := 0, 0
		for _, r := range results {
			if r.Err != nil {
				failures++
				continue
			}
			if r.UpdateAvailable && !r.Pinned {
				updates++
			}
		}
		if notify && updates > 0 && c.win != nil && !c.win.IsFocused() {
			n := mygo.NewNotification(mygo.NotificationOptions{
				Title: "ghpm",
				Body:  fmt.Sprintf("%d update(s) available", updates),
			})
			go func() { _ = n.Show() }()
		}
		msg := fmt.Sprintf("%d app(s) checked, %d update(s) available", len(results), updates)
		if failures > 0 {
			return msg, fmt.Errorf("%d check(s) failed", failures)
		}
		return msg, nil
	})
}

func (c *controller) updateAll() {
	c.startJob("Checking and updating", func(ctx context.Context, pf app.ProgressFunc) (string, error) {
		results := c.be.CheckFresh(ctx, nil, pf)
		var ids []string
		for _, r := range results {
			if r.Err == nil && r.UpdateAvailable && !r.Pinned {
				ids = append(ids, r.ID)
			}
		}
		if len(ids) == 0 {
			return "Everything is up to date", nil
		}
		installed := c.be.InstallMany(ctx, ids, app.InstallOptions{Progress: pf})
		return installSummary(installed), installError(installed)
	})
}

func (c *controller) installSelected() {
	v := c.selectedView()
	if v == nil {
		return
	}
	id := v.ID
	c.startJob("Installing "+id, func(ctx context.Context, pf app.ProgressFunc) (string, error) {
		r := c.be.InstallOne(ctx, id, app.InstallOptions{Progress: pf})
		if r.Err != nil {
			return "", r.Err
		}
		msg := fmt.Sprintf("%s %s", id, r.Version)
		if r.Previous != "" {
			msg = fmt.Sprintf("%s %s → %s", id, r.Previous, r.Version)
		}
		if len(r.Warnings) > 0 {
			msg += " · " + r.Warnings[0]
		}
		return msg, nil
	})
}

func (c *controller) addApp() {
	repo := strings.TrimSpace(c.addRepo)
	if repo == "" {
		c.addErr = "Enter a GitHub repository"
		c.invalidate()
		return
	}
	id := strings.TrimSpace(c.addID)
	channel := "latest"
	if c.addPrerelease {
		channel = "prerelease"
	}
	install := c.addInstall
	c.showAdd = false
	c.addErr = ""
	c.startJob("Adding "+repo, func(ctx context.Context, pf app.ProgressFunc) (string, error) {
		res, err := c.be.Add(ctx, app.AddOptions{
			Input: repo, ID: id, Channel: channel, Install: install, Progress: pf,
		})
		if err != nil {
			return "", err
		}
		if res.Installed != nil && res.Installed.Err != nil {
			return "", res.Installed.Err
		}
		msg := "Added " + res.App.ID
		if res.Installed != nil {
			msg += " " + res.Installed.Version
		}
		return msg, nil
	})
}

func (c *controller) togglePin() {
	v := c.selectedView()
	if v == nil || !v.Installed {
		return
	}
	if err := c.be.SetPinned(v.ID, !v.Pinned); err != nil {
		c.status = err.Error()
		c.statusErr = true
		c.invalidate()
		return
	}
	if v.Pinned {
		c.status = v.ID + " unpinned"
	} else {
		c.status = v.ID + " pinned"
	}
	c.statusErr = false
	c.refresh()
}

func (c *controller) rollbackSelected() {
	v := c.selectedView()
	if v == nil || !v.Installed {
		return
	}
	if len(versionsOf(*v)) < 2 {
		c.status = "No earlier version of " + v.ID + " is installed"
		c.statusErr = true
		c.invalidate()
		return
	}
	res, err := c.be.Rollback(v.ID, "")
	if err != nil {
		c.status = err.Error()
		c.statusErr = true
	} else {
		c.status = fmt.Sprintf("%s rolled back to %s", v.ID, res.Version)
		c.statusErr = false
	}
	c.refresh()
}

func (c *controller) askRemove() {
	v := c.selectedView()
	if v == nil {
		return
	}
	c.removeID = v.ID
	c.removeInstalled = v.Installed
	c.showRemove = true
	c.invalidate()
}

func (c *controller) remove(purge bool) {
	id := c.removeID
	c.showRemove = false
	c.startJob("Removing "+id, func(ctx context.Context, _ app.ProgressFunc) (string, error) {
		if _, err := c.be.Remove(id, purge); err != nil {
			return "", err
		}
		if purge {
			return id + " removed", nil
		}
		return id + " uninstalled (config kept)", nil
	})
}

func (c *controller) openPath(path string) {
	if path == "" {
		return
	}
	if err := mygo.Shell.OpenPath(path); err != nil {
		c.status = err.Error()
		c.statusErr = true
		c.invalidate()
	}
}
