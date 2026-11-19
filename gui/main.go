// Command ghpm-gui is the native desktop interface for ghpm. It uses MyGo's
// native UI toolkit (no WebView, no cgo) and drives the same internal/app
// service as the CLI, TUI and local web UI.
package main

import (
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"ghpm/internal/app"
	"ghpm/internal/version"
)

func main() {
	mygo.App.SetName("ghpm-gui")
	if mygo.IsDev() {
		mygo.App.SetVersion(version.String())
	}

	if !mygo.App.RequestSingleInstanceLock() {
		return // the running instance was brought to the front
	}

	svc, err := app.New(app.Options{})
	if err != nil {
		log.Fatalf("ghpm: %v", err)
	}
	ctrl := newController(serviceBackend{svc})

	mygo.App.OnSecondInstance(func([]string, string) {
		if ctrl.win != nil {
			ctrl.win.Restore()
			ctrl.win.Focus()
		}
	})

	mygo.App.WhenReady(func() {
		ctrl.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "ghpm",
			Width:     1180,
			Height:    780,
			MinWidth:  900,
			MinHeight: 520,
			Content:   ui.View(ctrl.view),
		})
		installMenu(ctrl)
		ctrl.refresh()
		ctrl.checkAll(false)
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatalf("ghpm: %v", err)
	}
}

func showAddDialog(c *controller) {
	c.addRepo, c.addID, c.addErr = "", "", ""
	c.addInstall, c.addPrerelease = true, false
	c.showAdd = true
	c.invalidate()
}

func installMenu(c *controller) {
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Label: "File", Submenu: []*mygo.MenuItem{
			{Label: "Add Application…", Accelerator: "CmdOrCtrl+N",
				Click: func(*mygo.MenuItem, *mygo.Window) { showAddDialog(c) }},
			{Label: "Check for Updates", Accelerator: "CmdOrCtrl+R",
				Click: func(*mygo.MenuItem, *mygo.Window) { c.checkAll(true) }},
			{Label: "Update All",
				Click: func(*mygo.MenuItem, *mygo.Window) { c.updateAll() }},
			mygo.Separator(),
			{Label: "Refresh", Accelerator: "CmdOrCtrl+Shift+R",
				Click: func(*mygo.MenuItem, *mygo.Window) { c.refresh() }},
			mygo.Separator(),
			{Label: "Open Config File",
				Click: func(*mygo.MenuItem, *mygo.Window) { c.openPath(c.be.ConfigPath()) }},
			{Label: "Open Data Folder",
				Click: func(*mygo.MenuItem, *mygo.Window) { c.openPath(c.be.DataDir()) }},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}},
		{Role: mygo.RoleEditMenu},
		{Label: "View", Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleToggleFullScreen},
		}},
		{Label: "Help", Submenu: []*mygo.MenuItem{
			{Label: "ghpm on GitHub",
				Click: func(*mygo.MenuItem, *mygo.Window) {
					_ = mygo.Shell.OpenExternal("https://github.com/yangjinhe/ghpm")
				}},
		}},
		{Role: mygo.RoleWindowMenu},
	}))
}
