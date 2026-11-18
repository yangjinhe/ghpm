package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSetLoadDelete(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rec := &AppState{
		ID: "tool", Repo: "a/b", Version: "1.0.0", InstalledAt: time.Now(),
		Versions: []string{"0.9.0", "1.0.0"}, Pinned: true,
	}
	if err := s.Set(rec); err != nil {
		t.Fatal(err)
	}
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	got := st.Apps["tool"]
	if got == nil || got.Version != "1.0.0" || !got.Pinned || len(got.Versions) != 2 {
		t.Fatalf("record = %+v", got)
	}
	if err := s.Delete("tool"); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Load()
	if len(st.Apps) != 0 {
		t.Errorf("apps = %v", st.Apps)
	}
}

func TestConcurrentUpdates(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := s.Update(func(st *State) error {
				st.Apps[fmt.Sprintf("app%d", i)] = &AppState{ID: fmt.Sprintf("app%d", i)}
				return nil
			})
			if err != nil {
				t.Errorf("update %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Apps) != n {
		t.Errorf("lost updates: %d of %d", len(st.Apps), n)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := &AppState{ID: "tool", Repo: "a/b", Version: "2.0.0", InstalledAt: time.Now().UTC()}
	if err := s.Set(rec); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	st, err := s2.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Apps["tool"]; got == nil || got.Version != "2.0.0" {
		t.Fatalf("reopened record = %+v", got)
	}
}

func TestTouchCheck(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Set(&AppState{ID: "tool", Repo: "a/b", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchCheck("tool", "1.1.0", "v1.1.0", nil); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Load()
	rec := st.Apps["tool"]
	if rec.LatestVersion != "1.1.0" || rec.LastChecked.IsZero() || rec.CheckError != "" {
		t.Errorf("check state = %+v", rec)
	}
	if err := s.TouchCheck("tool", "", "", fmt.Errorf("boom")); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Load()
	if rec := st.Apps["tool"]; rec.CheckError != "boom" {
		t.Errorf("check error = %+v", rec)
	}
}

func TestMigrateLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	installed := time.Date(2026, 11, 18, 3, 4, 5, 123456789, time.UTC)
	checked := installed.Add(time.Hour)
	legacy := New()
	legacy.Apps["tool"] = &AppState{
		ID: "tool", Repo: "acme/tool", Channel: "latest", Tag: "v1.2.3",
		Version: "1.2.3", Asset: "tool.tar.gz", AssetURL: "https://example/x",
		SHA256: strings.Repeat("a", 64), InstallType: "archive",
		BinPath: "/data/tool/1.2.3/tool", BinRel: "tool", LinkPath: "/bin/tool",
		Versions: []string{"1.1.0", "1.2.3"}, Pinned: true,
		InstalledAt: installed, UpdatedAt: checked,
		LatestVersion: "1.3.0", LatestTag: "v1.3.0", LastChecked: checked,
		CheckError: "rate limited",
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(legacyPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath+".lock", nil, 0o644); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(dir, "state.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	rec := st.Apps["tool"]
	if rec == nil {
		t.Fatal("legacy record not imported")
	}
	if rec.Version != "1.2.3" || !rec.Pinned || len(rec.Versions) != 2 || rec.CheckError != "rate limited" {
		t.Errorf("imported record = %+v", rec)
	}
	if !rec.InstalledAt.Equal(installed) || !rec.UpdatedAt.Equal(checked) || !rec.LastChecked.Equal(checked) {
		t.Errorf("timestamps = %v %v %v", rec.InstalledAt, rec.UpdatedAt, rec.LastChecked)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Errorf("legacy state.json still present: %v", err)
	}
	if _, err := os.Stat(legacyPath + ".lock"); !os.IsNotExist(err) {
		t.Errorf("legacy lock still present: %v", err)
	}

	// Reopening must not re-import or fail.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if st, err = s2.Load(); err != nil || st.Apps["tool"] == nil {
		t.Fatalf("reopen: %v %+v", err, st)
	}
}

func TestCorruptLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(legacyPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "state.db")
	if _, err := Open(dbPath); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("want corrupt legacy error, got %v", err)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Errorf("legacy file must be kept on failure: %v", err)
	}

	// Fixing the file lets the next start migrate normally.
	good := New()
	good.Apps["tool"] = &AppState{ID: "tool", Repo: "a/b", Version: "1.0.0"}
	data, _ := json.Marshal(good)
	if err := os.WriteFile(legacyPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, _ := s.Load()
	if st.Apps["tool"] == nil {
		t.Fatal("record not imported after fixing legacy JSON")
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Errorf("legacy state.json still present: %v", err)
	}
}

func TestNoLegacyCreatesEmptyDB(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := s.Load()
	if err != nil || len(st.Apps) != 0 {
		t.Fatalf("fresh state = %+v, %v", st, err)
	}
}

func TestPathWithSpaces(t *testing.T) {
	// macOS uses "~/Library/Application Support/ghpm".
	dir := filepath.Join(t.TempDir(), "Application Support", "ghpm")
	s, err := Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Set(&AppState{ID: "tool", Repo: "a/b", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	st, err := s.Load()
	if err != nil || st.Apps["tool"] == nil {
		t.Fatalf("load = %+v, %v", st, err)
	}
}
