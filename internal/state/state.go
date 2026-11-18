// Package state persists what is installed in a SQLite database. The
// database is created on first use; installations recorded by older ghpm
// versions in state.json are imported automatically and the JSON file is
// removed.
package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
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

// State is the whole metadata document. It is kept as a map in memory; the
// on-disk representation is the apps table.
type State struct {
	Schema int                  `json:"schema"`
	Apps   map[string]*AppState `json:"apps"`
}

func New() *State { return &State{Schema: 1, Apps: map[string]*AppState{}} }

const (
	schemaVersion = 1
	driverName    = "sqlite"
)

// Store owns the SQLite database.
type Store struct {
	path string
	db   *sql.DB
	mu   sync.Mutex // serialises writes inside this process
}

// Open prepares the database, creating it (and importing a legacy
// state.json from the same directory) when needed.
func Open(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	// Migration must be serialised across processes; SQLite itself takes over
	// once the database is open.
	unlock, err := lockFile(abs + ".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()

	db, err := sql.Open(driverName, dsn(abs))
	if err != nil {
		return nil, err
	}
	// A single connection keeps PRAGMAs and writes predictable; SQLite's file
	// locking plus busy_timeout handles CLI/TUI/web concurrency.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{path: abs, db: db}
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.migrateLegacyJSON(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Path returns the database location.
func (s *Store) Path() string { return s.path }

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

func dsn(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	// DELETE journal keeps the data directory to a single state.db file and
	// still gives crash-safe transactions; writes are short and infrequent.
	q.Add("_pragma", "journal_mode(DELETE)")
	q.Add("_pragma", "synchronous(NORMAL)")
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Store) initSchema() error {
	ctx := context.Background()
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS apps (
			id TEXT PRIMARY KEY,
			repo TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '',
			tag TEXT NOT NULL DEFAULT '',
			version TEXT NOT NULL DEFAULT '',
			asset TEXT NOT NULL DEFAULT '',
			asset_url TEXT NOT NULL DEFAULT '',
			sha256 TEXT NOT NULL DEFAULT '',
			install_type TEXT NOT NULL DEFAULT '',
			bin_path TEXT NOT NULL DEFAULT '',
			bin_rel TEXT NOT NULL DEFAULT '',
			link_path TEXT NOT NULL DEFAULT '',
			versions TEXT NOT NULL DEFAULT '[]',
			pinned INTEGER NOT NULL DEFAULT 0,
			installed_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT '',
			latest_version TEXT NOT NULL DEFAULT '',
			latest_tag TEXT NOT NULL DEFAULT '',
			last_checked TEXT NOT NULL DEFAULT '',
			check_error TEXT NOT NULL DEFAULT ''
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("init state db: %w", err)
		}
	}
	var current int
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema'`).Scan(&current)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('schema',?)`, schemaVersion); err != nil {
			return err
		}
	case err != nil:
		return err
	case current > schemaVersion:
		return fmt.Errorf("state database schema %d is newer than this ghpm supports (%d)", current, schemaVersion)
	}
	return nil
}

// migrateLegacyJSON imports state.json next to the database, then removes it.
// A meta flag makes the import idempotent, so a crash between commit and file
// removal cannot double-import or lose data.
func (s *Store) migrateLegacyJSON() error {
	ctx := context.Background()
	var migrated string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='json_migrated'`).Scan(&migrated)
	legacy := filepath.Join(filepath.Dir(s.path), "state.json")
	if err == nil && migrated == "1" {
		// A previous run may have migrated but failed to unlink the JSON.
		_ = os.Remove(legacy)
		_ = os.Remove(legacy + ".lock")
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	data, readErr := os.ReadFile(legacy)
	if errors.Is(readErr, os.ErrNotExist) {
		_, err := s.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('json_migrated','1')
			ON CONFLICT(key) DO UPDATE SET value='1'`)
		return err
	}
	if readErr != nil {
		return fmt.Errorf("read legacy state %s: %w", legacy, readErr)
	}

	old := New()
	if err := json.Unmarshal(data, old); err != nil {
		return fmt.Errorf("legacy state %s is corrupt: %w", legacy, err)
	}
	if old.Apps == nil {
		old.Apps = map[string]*AppState{}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM apps`); err != nil {
		tx.Rollback()
		return err
	}
	if err := insertApps(ctx, tx, old); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('json_migrated','1')
		ON CONFLICT(key) DO UPDATE SET value='1'`); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	if err := os.Remove(legacy); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("state migrated to %s, but could not remove %s: %w", s.path, legacy, err)
	}
	_ = os.Remove(legacy + ".lock")
	return nil
}

// Load reads the state without locking. Callers should treat the result as a
// snapshot.
func (s *Store) Load() (*State, error) {
	rows, err := s.db.Query(`SELECT ` + appColumns + ` FROM apps`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	st := New()
	for rows.Next() {
		rec, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		st.Apps[rec.ID] = rec
	}
	return st, rows.Err()
}

// Update runs fn under a write transaction against a freshly loaded state
// and saves the result atomically if fn succeeds.
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT ` + appColumns + ` FROM apps`)
	if err != nil {
		return err
	}
	st := New()
	for rows.Next() {
		rec, err := scanApp(rows)
		if err != nil {
			rows.Close()
			return err
		}
		st.Apps[rec.ID] = rec
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if err := fn(st); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM apps`); err != nil {
		return err
	}
	if err := insertApps(ctx, tx, st); err != nil {
		return err
	}
	return tx.Commit()
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

const appColumns = `id, repo, channel, tag, version, asset, asset_url, sha256,
	install_type, bin_path, bin_rel, link_path, versions, pinned,
	installed_at, updated_at, latest_version, latest_tag, last_checked, check_error`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanApp(row rowScanner) (*AppState, error) {
	var (
		rec                       AppState
		versions                  string
		pinned                    int
		installed, updated, check string
	)
	if err := row.Scan(
		&rec.ID, &rec.Repo, &rec.Channel, &rec.Tag, &rec.Version, &rec.Asset,
		&rec.AssetURL, &rec.SHA256, &rec.InstallType, &rec.BinPath, &rec.BinRel,
		&rec.LinkPath, &versions, &pinned,
		&installed, &updated, &rec.LatestVersion, &rec.LatestTag, &check, &rec.CheckError,
	); err != nil {
		return nil, err
	}
	rec.Pinned = pinned != 0
	if versions != "" {
		if err := json.Unmarshal([]byte(versions), &rec.Versions); err != nil {
			return nil, fmt.Errorf("app %q: corrupt versions column: %w", rec.ID, err)
		}
	}
	var err error
	if rec.InstalledAt, err = parseTime(installed); err != nil {
		return nil, fmt.Errorf("app %q: installed_at: %w", rec.ID, err)
	}
	if rec.UpdatedAt, err = parseTime(updated); err != nil {
		return nil, fmt.Errorf("app %q: updated_at: %w", rec.ID, err)
	}
	if rec.LastChecked, err = parseTime(check); err != nil {
		return nil, fmt.Errorf("app %q: last_checked: %w", rec.ID, err)
	}
	return &rec, nil
}

func insertApps(ctx context.Context, tx *sql.Tx, st *State) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO apps (`+appColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			repo=excluded.repo, channel=excluded.channel, tag=excluded.tag,
			version=excluded.version, asset=excluded.asset, asset_url=excluded.asset_url,
			sha256=excluded.sha256, install_type=excluded.install_type,
			bin_path=excluded.bin_path, bin_rel=excluded.bin_rel,
			link_path=excluded.link_path, versions=excluded.versions,
			pinned=excluded.pinned, installed_at=excluded.installed_at,
			updated_at=excluded.updated_at, latest_version=excluded.latest_version,
			latest_tag=excluded.latest_tag, last_checked=excluded.last_checked,
			check_error=excluded.check_error`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, rec := range st.Apps {
		if rec == nil {
			continue
		}
		versions, err := json.Marshal(rec.Versions)
		if err != nil {
			return err
		}
		if rec.Versions == nil {
			versions = []byte("[]")
		}
		if _, err := stmt.ExecContext(ctx,
			rec.ID, rec.Repo, rec.Channel, rec.Tag, rec.Version, rec.Asset,
			rec.AssetURL, rec.SHA256, rec.InstallType, rec.BinPath, rec.BinRel,
			rec.LinkPath, string(versions), boolInt(rec.Pinned),
			formatTime(rec.InstalledAt), formatTime(rec.UpdatedAt),
			rec.LatestVersion, rec.LatestTag, formatTime(rec.LastChecked), rec.CheckError,
		); err != nil {
			return err
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
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
