package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"ghpm/internal/app"
	"ghpm/internal/config"
	"ghpm/internal/state"
)

func newTestModel(t *testing.T) *model {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	paths := config.DefaultPaths()
	paths.ConfigFile = filepath.Join(dir, "config.yaml")
	paths.DataDir = filepath.Join(dir, "data")
	paths.BinDir = filepath.Join(dir, "bin")
	cfg := config.DefaultsConfig(paths)
	cfg.Path = paths.ConfigFile
	cfg.Source = paths
	cfg.Defaults.DataDir = paths.DataDir
	cfg.Defaults.BinDir = paths.BinDir
	cfg.Apps = []config.App{{ID: "tool", Repo: "acme/tool"}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	svc, err := app.New(app.Options{ConfigPath: paths.ConfigFile})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.Set(&state.AppState{
		ID: "tool", Repo: "acme/tool", Version: "1.0.0", Tag: "v1.0.0",
		LatestVersion: "1.1.0", LatestTag: "v1.1.0", InstalledAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	views, err := svc.Views()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := newModel(ctx, cancel, svc)
	m.width, m.height = 100, 30
	m.views = views
	return m
}

func TestViewRendersList(t *testing.T) {
	m := newTestModel(t)
	out := m.View()
	for _, want := range []string{"ghpm", "tool", "1.0.0", "1.1.0", "update", "select"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
}

func TestViewEmptyAndModes(t *testing.T) {
	m := newTestModel(t)
	m.views = nil
	if out := m.View(); !strings.Contains(out, "No apps configured") {
		t.Errorf("empty view: %s", out)
	}
	m.mode = modeHelp
	if out := m.View(); !strings.Contains(out, "Keyboard shortcuts") {
		t.Errorf("help view: %s", out)
	}
	m.mode = modeConfirm
	m.confirmMsg = "Remove tool?"
	if out := m.View(); !strings.Contains(out, "Remove tool?") {
		t.Errorf("confirm view: %s", out)
	}
	m.mode = modeAdd
	if out := m.View(); !strings.Contains(out, "Add a GitHub repository") {
		t.Errorf("add view: %s", out)
	}
	m.mode = modeDetail
	detail := m.viewsAppView()
	m.detail = &detail
	if out := m.View(); out == "" {
		t.Error("detail view empty")
	}
}

// viewsAppView returns any view for detail rendering; when there is none we
// fabricate one.
func (m *model) viewsAppView() app.AppView {
	if len(m.views) > 0 {
		return m.views[0]
	}
	return app.AppView{ID: "tool", Repo: "acme/tool"}
}

func TestNavigationAndSelection(t *testing.T) {
	m := newTestModel(t)
	// Add a second row.
	m.views = append(m.views, app.AppView{ID: "other", Repo: "acme/other"})
	m, _ = update(m, keyMsg("down"))
	if m.cursor != 1 {
		t.Errorf("cursor = %d", m.cursor)
	}
	m, _ = update(m, keyMsg(" "))
	if !m.selected["other"] {
		t.Error("space did not select")
	}
	if ids := m.targetIDs(); len(ids) != 1 || ids[0] != "other" {
		t.Errorf("targets = %v", ids)
	}
	m, _ = update(m, keyMsg("esc"))
	if len(m.selected) != 0 {
		t.Error("esc should clear selection")
	}
	m.mode = modeHelp
	m, _ = update(m, keyMsg("x"))
	if m.mode != modeList {
		t.Error("help should close on any key")
	}
}

func TestAddWizardInput(t *testing.T) {
	m := newTestModel(t)
	m, _ = update(m, keyMsg("a"))
	if m.mode != modeAdd {
		t.Fatal("a should open the add wizard")
	}
	for _, r := range "acme/tool" {
		m, _ = update(m, teaKeyRune(r))
	}
	if got := m.addInput.Value(); got != "acme/tool" {
		t.Errorf("input = %q", got)
	}
	m, _ = update(m, keyMsg("esc"))
	if m.mode != modeList {
		t.Error("esc should close the wizard")
	}
}

func update(m *model, msg any) (*model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(*model), cmd
}
