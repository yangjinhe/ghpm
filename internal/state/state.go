// Package state persists what is installed. Writes are atomic and guarded by
// an advisory file lock so the CLI and TUI can run side by side.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// AppState is the record for one installed application.
type AppState struct {
	ID            string    `json:"id"`
	Repo          string    `json:"repo"`
	Channel       string    `json:"channel,omitempty"`
	Tag           string    `json:"tag"`
	Version       string    `json:"version"`
	Asset         string    `json:"asset"`
	AssetURL      string    `json:"asset_url,omitempty"`
	SHA256        string    `json:"sha256,omitempty"`
	InstallType   string    `json:"install_type"`
	BinPath       string    `json:"bin_path,omitempty"`  // absolute path inside the version dir
	BinRel        string    `json:"bin_rel,omitempty"`   // path relative to the version dir
	LinkPath      string    `json:"link_path,omitempty"` // absolute symlink in bin_dir
	Versions      []string  `json:"versions,omitempty"`
	Pinned        bool      `json:"pinned,omitempty"`
	InstalledAt   time.Time `json:"installed_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	LatestVersion string    `json:"latest_version,omitempty"`
	LatestTag     string    `json:"latest_tag,omitempty"`
	LastChecked   time.Time `json:"last_checked,omitempty"`
	CheckError    string    `json:"check_error,omitempty"`
}

// State is the on-disk document.
type State struct {
	Schema int                  `json:"schema"`
	Apps   map[string]*AppState `json:"apps"`
}

func New() *State { return &State{Schema: 1, Apps: map[string]*AppState{}} }

// Store owns the state file.
type Store struct {
	path string
	mu   sync.Mutex
}

// Open prepares a store, creating the directory if needed.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &Store{path: path}, nil
}

// Path returns the state file location.
func (s *Store) Path() string { return s.path }

// Load reads the state without locking. Callers should treat the result as a
// snapshot.
func (s *Store) Load() (*State, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	st := New()
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("state file %s is corrupt: %w", s.path, err)
	}
	if st.Apps == nil {
		st.Apps = map[string]*AppState{}
	}
	return st, nil
}

// Update runs fn under an exclusive lock against a freshly loaded state and
// saves the result atomically if fn succeeds.
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	unlock, err := lockFile(s.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

	st, err := s.Load()
	if err != nil {
		return err
	}
	if err := fn(st); err != nil {
		return err
	}
	return saveAtomic(s.path, st)
}

// Set installs or replaces one app record.
func (s *Store) Set(rec *AppState) error {
	return s.Update(func(st *State) error {
		st.Apps[rec.ID] = rec
		return nil
	})
}

// Delete removes an app record.
func (s *Store) Delete(id string) error {
	return s.Update(func(st *State) error {
		delete(st.Apps, id)
		return nil
	})
}

// TouchCheck stores the result of an update check.
func (s *Store) TouchCheck(id, latestVersion, latestTag string, checkErr error) error {
	return s.Update(func(st *State) error {
		rec, ok := st.Apps[id]
		if !ok {
			return nil
		}
		rec.LastChecked = time.Now().UTC()
		if checkErr != nil {
			rec.CheckError = checkErr.Error()
			return nil
		}
		rec.CheckError = ""
		rec.LatestVersion = latestVersion
		rec.LatestTag = latestTag
		return nil
	})
}

func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func saveAtomic(path string, st *State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}
