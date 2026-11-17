package state

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSetLoadDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := &AppState{ID: "tool", Repo: "a/b", Version: "1.0.0", InstalledAt: time.Now()}
	if err := s.Set(rec); err != nil {
		t.Fatal(err)
	}
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Apps["tool"]; got == nil || got.Version != "1.0.0" {
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
	s, _ := Open(filepath.Join(dir, "state.json"))
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

func TestCorruptState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := Open(path)
	if _, err := s.Load(); err == nil {
		t.Fatal("want corrupt state error")
	}
}
