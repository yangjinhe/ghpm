package main

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func (c *controller) view(ctx *ui.Context) {
	t := ctx.Theme()
	ui.Column(ctx).Fill().Children(func() {
		c.header(ctx, t)
		c.toolbar(ctx, t)
		ui.Divider(ctx)
		c.body(ctx, t)
		ui.Divider(ctx)
		c.actions(ctx, t)
		c.progressView(ctx, t)
		ui.Divider(ctx)
		c.footer(ctx, t)
	})
	c.addModal(ctx, t)
	c.removeModal(ctx, t)
	c.detailsModal(ctx, t)
}

func (c *controller) header(ctx *ui.Context, t *ui.Theme) {
	installed, updates := counts(c.views)
	ui.Row(ctx).Padding(16, 18).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(ctx, "ghpm").FontSize(22).Bold()
		ui.Text(ctx, "GitHub release manager").FontSize(12).TextColor(t.TextMuted)
		ui.Text(ctx, "").Grow(1)
		ui.Text(ctx, fmt.Sprintf("%d apps · %d installed · %d updates", len(c.views), installed, updates)).
			FontSize(12).TextColor(t.TextMuted)
		ui.Text(ctx, mygo.App.Version()+" · "+runtime.GOOS+"/"+runtime.GOARCH).
			FontSize(11).TextColor(t.TextMuted)
	})
}

func (c *controller) toolbar(ctx *ui.Context, t *ui.Theme) {
	ui.Row(ctx).Padding(0, 18).Gap(8).AlignItems(ui.Center).Children(func() {
		if ui.TextInput(ctx, &c.query).Placeholder("Filter apps").Label("Filter apps").Width(260).Changed() {
			c.applyFilter()
		}
		ui.Button(ctx, "Check for Updates").Disabled(c.busy).OnClick(func() { c.checkAll(true) })
		ui.PrimaryButton(ctx, "Update All").Disabled(c.busy).OnClick(c.updateAll)
		ui.Button(ctx, "Add App…").Disabled(c.busy).OnClick(func() { showAddDialog(c) })
		ui.Text(ctx, "").Grow(1)
		ui.Button(ctx, "Refresh").Disabled(c.busy).OnClick(c.refresh)
	})
}

func (c *controller) body(ctx *ui.Context, t *ui.Theme) {
	switch {
	case len(c.views) == 0:
		ui.Column(ctx).Grow(1).Center().Gap(10).Children(func() {
			ui.Text(ctx, "No applications configured").FontSize(16).Bold()
			ui.Text(ctx, "Add a GitHub repository to get started.").TextColor(t.TextMuted)
			ui.PrimaryButton(ctx, "Add Application…").OnClick(func() { showAddDialog(c) })
		})
		return
	case len(c.rows) == 0:
		ui.Column(ctx).Grow(1).Center().Gap(6).Children(func() {
			ui.Text(ctx, "No apps match the filter").FontSize(14)
			ui.Text(ctx, "Clear the search field to see all apps.").TextColor(t.TextMuted)
		})
		return
	}

	c.table.Selected = &c.selected
	c.table.Key = func(i int) any { return c.views[c.rows[i]].ID }
	columns := []ui.TableColumn{
		{Title: "ID", Width: 190},
		{Title: "Installed", Width: 110},
		{Title: "Latest", Width: 130},
		{Title: "Status", Width: 170},
		{Title: "Repository"},
	}
	tbl := ui.Table(ctx, &c.table, columns, len(c.rows), func(row, col int) {
		c.cell(ctx, t, row, col)
	}).Grow(1)
	if tbl.Submitted() && c.selectedView() != nil {
		c.showDetails = true
	}
}

func (c *controller) cell(ctx *ui.Context, t *ui.Theme, row, col int) {
	v := &c.views[c.rows[row]]
	primary, muted := t.Text, t.TextMuted
	selected := row == c.selected
	if selected {
		// Explicit colors otherwise hide the row's accent background.
		primary, muted = t.AccentText, t.AccentText.Alpha(0.8)
	}
	switch col {
	case 0:
		ui.Text(ctx, v.ID).TextColor(primary).SingleLine()
	case 1:
		text := "—"
		if v.Installed {
			text = v.CurrentVersion
		}
		ui.Text(ctx, text).TextColor(primary).SingleLine()
	case 2:
		text := "—"
		if v.LatestTag != "" {
			text = v.LatestTag
		} else if v.LatestVersion != "" {
			text = v.LatestVersion
		}
		ui.Text(ctx, text).TextColor(primary).SingleLine()
	case 3:
		color := muted
		if !selected {
			switch {
			case v.CheckError != "":
				color = t.Danger
			case v.UpdateAvailable:
				color = t.Warning
			case v.Installed && v.LatestVersion != "":
				color = t.Success
			}
		}
		ui.Text(ctx, appStatus(*v)).TextColor(color).SingleLine()
	case 4:
		ui.Text(ctx, v.Repo).TextColor(muted).SingleLine()
	}
}

func (c *controller) actions(ctx *ui.Context, t *ui.Theme) {
	ui.Row(ctx).Padding(10, 18).Gap(8).AlignItems(ui.Center).Children(func() {
		v := c.selectedView()
		if v == nil {
			ui.Text(ctx, "Select an app to act on it").TextColor(t.TextMuted).Grow(1)
			return
		}
		switch {
		case !v.Installed:
			ui.PrimaryButton(ctx, "Install").Disabled(c.busy).OnClick(c.installSelected)
		case v.UpdateAvailable:
			label := "Update"
			if v.LatestTag != "" {
				label = "Update to " + v.LatestTag
			}
			ui.PrimaryButton(ctx, label).Disabled(c.busy).OnClick(c.installSelected)
		default:
			ui.Text(ctx, "Up to date").TextColor(t.Success)
		}
		pin := "Pin"
		if v.Pinned {
			pin = "Unpin"
		}
		ui.Button(ctx, pin).Disabled(c.busy || !v.Installed).OnClick(c.togglePin)
		ui.Button(ctx, "Rollback").Disabled(c.busy || len(versionsOf(*v)) < 2).OnClick(c.rollbackSelected)
		ui.Button(ctx, "Details").OnClick(func() { c.showDetails = true })
		ui.Text(ctx, "").Grow(1)
		ui.Button(ctx, "Remove…").Disabled(c.busy).OnClick(c.askRemove)
	})
}

func (c *controller) progressView(ctx *ui.Context, t *ui.Theme) {
	if len(c.order) == 0 {
		return
	}
	start := 0
	if len(c.order) > 6 {
		start = len(c.order) - 6
	}
	ui.Divider(ctx)
	ui.Column(ctx).Padding(8, 18).Gap(6).Children(func() {
		for _, id := range c.order[start:] {
			p := c.progress[id]
			if p == nil {
				continue
			}
			value := float64(0)
			switch p.Phase {
			case "downloading":
				value = p.Percent / 100
			case "resolving", "installing", "verifying":
				value = -1
			case "done":
				value = 1
			}
			text := p.Phase
			switch p.Phase {
			case "downloading":
				text = fmt.Sprintf("%.0f%% · %s", p.Percent, humanRate(p.Speed))
			case "checking":
				text = "checking…"
			case "checked", "done":
				if p.Message != "" {
					text = p.Message
				}
			case "error":
				text = "error: " + p.Err
			}
			color := t.TextMuted
			switch p.Phase {
			case "error":
				color = t.Danger
			case "done", "checked":
				color = t.Success
			}
			ui.Row(ctx).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(ctx, p.ID).Width(150).SingleLine()
				ui.Progress(ctx, value).Grow(1)
				ui.Text(ctx, text).FontSize(11).TextColor(color).Width(240).SingleLine()
			})
		}
	})
}

func (c *controller) footer(ctx *ui.Context, t *ui.Theme) {
	ui.Row(ctx).Padding(8, 18).Gap(10).AlignItems(ui.Center).Children(func() {
		if c.busy {
			ui.Spinner(ctx)
		}
		text := c.status
		if text == "" {
			text = "Ready"
		}
		color := t.TextMuted
		if c.statusErr {
			color = t.Danger
		}
		ui.Text(ctx, text).TextColor(color).SingleLine().Grow(1)
		token := "no token"
		if c.be.HasToken() {
			token = "token"
		}
		ui.Text(ctx, token).FontSize(11).TextColor(t.TextMuted).Tooltip("GITHUB_TOKEN: raises the API quota to 5000/hour")
		ui.Text(ctx, c.be.ConfigPath()).FontSize(11).TextColor(t.TextMuted).
			MaxWidth(420).SingleLine().Tooltip(c.be.ConfigPath())
	})
}

func (c *controller) addModal(ctx *ui.Context, t *ui.Theme) {
	ui.Modal(ctx, &c.showAdd, func() {
		ui.Column(ctx).Width(460).Gap(10).Children(func() {
			ui.Text(ctx, "Add Application").FontSize(18).Bold()
			ui.Text(ctx, "GitHub repository").FontSize(12).TextColor(t.TextMuted)
			if ui.TextInput(ctx, &c.addRepo).
				AutoFocus().
				Placeholder("owner/repo or https://github.com/owner/repo").
				Label("Repository").
				FillWidth().
				Submitted() {
				c.addApp()
			}
			if ui.TextInput(ctx, &c.addID).
				Placeholder("App id (optional)").
				Label("App id").
				FillWidth().
				Submitted() {
				c.addApp()
			}
			ui.Checkbox(ctx, &c.addInstall, "Install immediately")
			ui.Checkbox(ctx, &c.addPrerelease, "Track prereleases")
			if c.addErr != "" {
				ui.Text(ctx, c.addErr).TextColor(t.Danger)
			}
			ui.Row(ctx).Gap(8).Justify(ui.End).Children(func() {
				ui.Button(ctx, "Cancel").OnClick(func() { c.showAdd = false })
				ui.PrimaryButton(ctx, "Add").Disabled(c.busy).OnClick(c.addApp)
			})
		})
	})
}

func (c *controller) removeModal(ctx *ui.Context, t *ui.Theme) {
	ui.Modal(ctx, &c.showRemove, func() {
		ui.Column(ctx).Width(460).Gap(10).Children(func() {
			ui.Text(ctx, "Remove "+c.removeID+"?").FontSize(18).Bold()
			if c.removeInstalled {
				ui.Text(ctx, "Installed files, the symlink and the state record will be deleted.")
			} else {
				ui.Text(ctx, "This app is configured but not installed.")
			}
			ui.Text(ctx, "The configuration is kept unless you choose to delete it.").
				FontSize(12).TextColor(t.TextMuted)
			ui.Row(ctx).Gap(8).Justify(ui.End).Children(func() {
				ui.Button(ctx, "Cancel").OnClick(func() { c.showRemove = false })
				if c.removeInstalled {
					ui.Button(ctx, "Uninstall").OnClick(func() { c.remove(false) })
				}
				label := "Delete Config"
				if c.removeInstalled {
					label = "Uninstall & Delete Config"
				}
				ui.Button(ctx, label).OnClick(func() { c.remove(true) })
			})
		})
	})
}

func (c *controller) detailsModal(ctx *ui.Context, t *ui.Theme) {
	ui.Modal(ctx, &c.showDetails, func() {
		v := c.selectedView()
		if v == nil {
			c.showDetails = false
			return
		}
		ui.Column(ctx).Width(600).Gap(8).Children(func() {
			ui.Text(ctx, v.ID).FontSize(18).Bold()
			fieldView(ctx, t, "Repository", v.Repo)
			fieldView(ctx, t, "Channel", v.Channel)
			fieldView(ctx, t, "Status", appStatus(*v))
			fieldView(ctx, t, "Installed", v.CurrentVersion)
			fieldView(ctx, t, "Latest", v.LatestTag)
			fieldView(ctx, t, "Link", v.LinkPath)
			if v.State != nil {
				fieldView(ctx, t, "Binary", v.State.BinPath)
				fieldView(ctx, t, "Asset", v.State.Asset)
				fieldView(ctx, t, "SHA-256", v.State.SHA256)
				if !v.State.LastChecked.IsZero() {
					fieldView(ctx, t, "Checked", v.State.LastChecked.Local().Format("2006-01-02 15:04"))
				}
			}
			versions := versionsOf(*v)
			if len(versions) > 0 {
				fieldView(ctx, t, "Versions", strings.Join(versions, ", "))
			}
			ui.Row(ctx).Gap(8).Justify(ui.End).Children(func() {
				ui.Button(ctx, "Open Data Folder").OnClick(func() { c.openPath(c.be.DataDir()) })
				ui.Button(ctx, "Open Bin Folder").OnClick(func() { c.openPath(c.be.BinDir()) })
				ui.PrimaryButton(ctx, "Close").OnClick(func() { c.showDetails = false })
			})
		})
	})
}

func fieldView(ctx *ui.Context, t *ui.Theme, label, value string) {
	if value == "" {
		value = "—"
	}
	ui.Row(ctx).Gap(10).AlignItems(ui.Start).Children(func() {
		ui.Text(ctx, label).Width(100).TextColor(t.TextMuted)
		ui.Text(ctx, value).Selectable().Grow(1)
	})
}
