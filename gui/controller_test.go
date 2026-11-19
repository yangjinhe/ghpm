package main

import (
	"context"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"ghpm/internal/app"
	"ghpm/internal/config"
	"ghpm/internal/state"
)

type fakeBackend struct {
	views        []app.AppView
	checkCalls   int
	installCalls int
	addCalls     int
	removeCalls  int
	lastAdd      app.AddOptions
	lastRemove   struct {
		id    string
		purge bool
	}
}

func (f *fakeBackend) Views() ([]app.AppView, error) { return f.views, nil }

func (f *fakeBackend) CheckFresh(ctx context.Context, ids []string, progress app.ProgressFunc) []app.CheckResult {
	f.checkCalls++
	out := make([]app.CheckResult, 0, len(f.views))
	for i := range f.views {
		v := &f.views[i]
		v.LatestVersion, v.LatestTag = "1.2.0", "v1.2.0"
		v.UpdateAvailable = v.Installed && v.CurrentVersion != "1.2.0"
		out = append(out, app.CheckResult{
			ID: v.ID, Installed: v.Installed, InstalledVersion: v.CurrentVersion,
			LatestVersion: v.LatestVersion, LatestTag: v.LatestTag,
			UpdateAvailable: v.UpdateAvailable, Comparable: true,
		})
		if progress != nil {
			progress(app.Event{Kind: "check", ID: v.ID, Message: "1.2.0"})
		}
	}
	return out
}

func (f *fakeBackend) install(id string) app.InstallResult {
	f.installCalls++
	for i := range f.views {
		v := &f.views[i]
		if v.ID != id {
			continue
		}
		v.Installed = true
		v.CurrentVersion = "1.2.0"
		v.LatestVersion, v.LatestTag = "1.2.0", "v1.2.0"
		v.UpdateAvailable = false
		if v.State == nil {
			v.State = &state.AppState{ID: id}
		}
		v.State.Version = "1.2.0"
	}
	return app.InstallResult{ID: id, Version: "1.2.0"}
}

func (f *fakeBackend) InstallOne(ctx context.Context, id string, opts app.InstallOptions) app.InstallResult {
	res := f.install(id)
	if opts.Progress != nil {
		opts.Progress(app.Event{Kind: "done", ID: id, Message: id + " 1.2.0"})
	}
	return res
}

func (f *fakeBackend) InstallMany(ctx context.Context, ids []string, opts app.InstallOptions) []app.InstallResult {
	var out []app.InstallResult
	for _, id := range ids {
		out = append(out, f.InstallOne(ctx, id, opts))
	}
	return out
}

func (f *fakeBackend) Add(ctx context.Context, opts app.AddOptions) (*app.AddResult, error) {
	f.addCalls++
	f.lastAdd = opts
	id := opts.ID
	if id == "" {
		id = "newapp"
	}
	v := app.AppView{ID: id, Repo: opts.Input, Channel: opts.Channel}
	if opts.Install {
		v.Installed = true
		v.CurrentVersion = "1.2.0"
		v.State = &state.AppState{ID: id, Version: "1.2.0"}
	}
	f.views = append(f.views, v)
	return &app.AddResult{App: config.App{ID: id, Repo: opts.Input}, Installed: nil}, nil
}

func (f *fakeBackend) Rollback(id, version string) (app.InstallResult, error) {
	return app.InstallResult{ID: id, Version: version}, nil
}

func (f *fakeBackend) SetPinned(id string, pinned bool) error {
	for i := range f.views {
		if f.views[i].ID == id {
			f.views[i].Pinned = pinned
		}
	}
	return nil
}

func (f *fakeBackend) Remove(id string, purge bool) ([]string, error) {
	f.removeCalls++
	f.lastRemove.id, f.lastRemove.purge = id, purge
	for i := range f.views {
		if f.views[i].ID != id {
			continue
		}
		if purge {
			f.views = append(f.views[:i], f.views[i+1:]...)
		} else {
			f.views[i].Installed = false
			f.views[i].CurrentVersion = ""
			f.views[i].State = nil
		}
		break
	}
	return nil, nil
}

func (f *fakeBackend) ConfigPath() string { return "/home/me/.config/ghpm/config.yaml" }
func (f *fakeBackend) DataDir() string    { return "/home/me/.local/share/ghpm" }
func (f *fakeBackend) BinDir() string     { return "/home/me/.local/bin" }
func (f *fakeBackend) AppsDir() string    { return "/Applications" }
func (f *fakeBackend) HasToken() bool     { return false }

func sampleViews() []app.AppView {
	return []app.AppView{
		{ID: "lazygit", Repo: "jesseduffield/lazygit", LatestVersion: "0.45.0", LatestTag: "v0.45.0"},
		{
			ID: "fd", Repo: "sharkdp/fd", Installed: true,
			CurrentVersion: "10.5.2", LatestVersion: "10.5.2", LatestTag: "v10.5.2",
			State: &state.AppState{ID: "fd", Version: "10.5.2", Versions: []string{"10.4.0", "10.5.2"}},
		},
	}
}

func newTestController(t *testing.T) (*controller, *fakeBackend) {
	t.Helper()
	f := &fakeBackend{views: sampleViews()}
	c := newController(f)
	c.async = func(fn func()) { fn() } // run operations inline in tests
	c.setViews(f.views)
	return c, f
}

func TestViewRendersAndFilters(t *testing.T) {
	c, _ := newTestController(t)
	tt := ui.NewTester(c.view, 1200, 800)
	tt.Frame()
	for _, want := range []string{"lazygit", "fd", "Check for Updates", "Update All", "Add App…"} {
		if !tt.HasText(want) {
			t.Errorf("view is missing %q", want)
		}
	}
	c.query = "fd"
	c.applyFilter()
	tt.Frame()
	if tt.HasText("lazygit") {
		t.Error("filter should hide lazygit")
	}
	if !tt.HasText("fd") {
		t.Error("filter should keep fd")
	}
}

func TestCheckButton(t *testing.T) {
	c, f := newTestController(t)
	tt := ui.NewTester(c.view, 1200, 800)
	tt.Frame()
	if err := tt.Click("Check for Updates"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if f.checkCalls != 1 {
		t.Fatalf("check calls = %d", f.checkCalls)
	}
	if !strings.Contains(c.status, "update") {
		t.Errorf("status = %q", c.status)
	}
}

func TestInstallSelected(t *testing.T) {
	c, f := newTestController(t)
	tt := ui.NewTester(c.view, 1200, 800)
	tt.Frame()
	if err := tt.Click("Install"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if f.installCalls != 1 {
		t.Fatalf("install calls = %d", f.installCalls)
	}
	if !strings.Contains(c.status, "lazygit") {
		t.Errorf("status = %q", c.status)
	}
}

func TestAddApplication(t *testing.T) {
	c, f := newTestController(t)
	tt := ui.NewTester(c.view, 1200, 800)
	tt.Frame()
	if err := tt.Click("Add App…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Type("acme/newapp")
	if err := tt.Click("Add"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if f.addCalls != 1 || f.lastAdd.Input != "acme/newapp" {
		t.Fatalf("add = %d, %+v", f.addCalls, f.lastAdd)
	}
}

func TestRemoveSelectedDeletesConfig(t *testing.T) {
	c, f := newTestController(t)
	c.selected = 1 // fd
	c.applyFilter()
	tt := ui.NewTester(c.view, 1200, 800)
	tt.Frame()
	if err := tt.Click("Remove…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Uninstall & Delete Config"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if f.removeCalls != 1 || f.lastRemove.id != "fd" || !f.lastRemove.purge {
		t.Fatalf("remove = %d, %+v", f.removeCalls, f.lastRemove)
	}
}

func TestStatusHelpers(t *testing.T) {
	views := sampleViews()
	if appStatus(views[0]) != "not installed" {
		t.Errorf("status = %q", appStatus(views[0]))
	}
	installed, updates := counts(views)
	if installed != 1 || updates != 0 {
		t.Errorf("counts = %d, %d", installed, updates)
	}
	if got := installSummary([]app.InstallResult{{ID: "a", Version: "1"}, {ID: "b", Err: context.Canceled}}); !strings.Contains(got, "1 app") {
		t.Errorf("summary = %q", got)
	}
	if err := installError([]app.InstallResult{{ID: "a"}, {ID: "b", Err: context.Canceled}}); err == nil {
		t.Error("expected install error")
	}
}

func TestSnapshot(t *testing.T) {
	path := os.Getenv("GHPM_GUI_SNAPSHOT")
	if path == "" {
		t.Skip("set GHPM_GUI_SNAPSHOT to write a PNG of the main window")
	}
	c, _ := newTestController(t)
	c.status = "2 app(s) checked, 1 update(s) available"
	tt := ui.NewTester(c.view, 1200, 800)
	tt.Frame()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotModes(t *testing.T) {
	path := os.Getenv("GHPM_GUI_SNAPSHOT")
	if path == "" {
		t.Skip("set GHPM_GUI_SNAPSHOT to write a PNG of the main window")
	}
	mode := os.Getenv("GHPM_GUI_SNAPSHOT_MODE")
	c, _ := newTestController(t)
	switch mode {
	case "add":
		c.showAdd = true
		c.addRepo = "t8y2/motty"
		c.addID = "motty"
	case "details":
		c.selected = 1
		c.applyFilter()
		c.views[1].LinkPath = "/home/me/.local/bin/fd"
		c.views[1].State.Asset = "fd-v10.5.2-x86_64-unknown-linux-gnu.tar.gz"
		c.views[1].State.BinPath = "/home/me/.local/share/ghpm/apps/fd/10.5.2/fd"
		c.views[1].State.SHA256 = "9f2c1e0b5b0a1c8d7e6f5a4b3c2d1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d"
		c.showDetails = true
	case "progress":
		c.busy = true
		c.status = "Installing fd…"
		c.progress["fd"] = &opProgress{ID: "fd", Phase: "downloading", Percent: 62, Speed: 3.1 * 1024 * 1024}
		c.progress["lazygit"] = &opProgress{ID: "lazygit", Phase: "checked", Message: "v0.45.0"}
		c.order = []string{"fd", "lazygit"}
	default:
		c.views[1].LatestVersion = "10.6.0"
		c.views[1].LatestTag = "v10.6.0"
		c.views[1].UpdateAvailable = true
		c.status = "2 app(s) checked, 1 update(s) available"
	}
	tt := ui.NewTester(c.view, 1200, 800)
	tt.Frame()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, tt.Image()); err != nil {
		t.Fatal(err)
	}
}
