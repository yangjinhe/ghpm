// Package web serves a local browser interface for ghpm. It reuses the same
// internal/app service as the CLI and TUI, so every action behaves
// identically. Long running work is executed as a job whose events can be
// followed over Server-Sent Events.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"ghpm/internal/app"
	"ghpm/internal/version"
)

//go:embed static
var staticFS embed.FS

// Server exposes ghpm over HTTP.
type Server struct {
	svc  *app.Service
	jobs *jobManager

	mu      sync.Mutex
	baseCtx context.Context
}

// New builds a Server for svc.
func New(svc *app.Service) *Server {
	return &Server{svc: svc, jobs: newJobManager(), baseCtx: context.Background()}
}

// Addr returns the URL path in the browser, useful for logging.
func Addr(ln net.Listener) string {
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return ln.Addr().String()
	}
	if host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// Serve accepts requests until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener, allowRemote bool) error {
	s.mu.Lock()
	s.baseCtx = ctx
	s.mu.Unlock()

	srv := &http.Server{
		Handler:           s.Handler(allowRemote),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// Handler returns the HTTP handler. Unless allowRemote is set, requests with
// non-loopback Host headers are refused (DNS rebinding protection) and writes
// must be JSON (which forces a CORS preflight for cross-origin callers).
func (s *Server) Handler(allowRemote bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apps", s.handleApps)
	mux.HandleFunc("GET /api/doctor", s.handleDoctor)
	mux.HandleFunc("POST /api/check", s.handleCheck)
	mux.HandleFunc("POST /api/install", s.handleInstall)
	mux.HandleFunc("POST /api/update", s.handleUpdate)
	mux.HandleFunc("POST /api/add", s.handleAdd)
	mux.HandleFunc("POST /api/rollback", s.handleRollback)
	mux.HandleFunc("POST /api/pin", s.handlePin)
	mux.HandleFunc("POST /api/remove", s.handleRemove)
	mux.HandleFunc("GET /api/jobs/{id}", s.handleJob)
	mux.HandleFunc("GET /api/jobs/{id}/events", s.handleJobEvents)

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-store")
		}
		files.ServeHTTP(w, r)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'")
		if !allowRemote && !isLoopbackHost(r.Host) {
			http.Error(w, "ghpm web only accepts loopback requests; start it with --allow-remote to change this", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost && !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
			http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" || host == "" {
		return host == "localhost"
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ---- response helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

// ---- app view DTOs ----

type appInfo struct {
	ID              string    `json:"id"`
	Repo            string    `json:"repo"`
	Channel         string    `json:"channel,omitempty"`
	Description     string    `json:"description,omitempty"`
	Installed       bool      `json:"installed"`
	CurrentVersion  string    `json:"current_version,omitempty"`
	LatestVersion   string    `json:"latest_version,omitempty"`
	LatestTag       string    `json:"latest_tag,omitempty"`
	Status          string    `json:"status"`
	UpdateAvailable bool      `json:"update_available"`
	Comparable      bool      `json:"comparable"`
	Pinned          bool      `json:"pinned"`
	LinkPath        string    `json:"link_path,omitempty"`
	LastChecked     time.Time `json:"last_checked,omitempty"`
	CheckError      string    `json:"check_error,omitempty"`
	Versions        []string  `json:"versions,omitempty"`
	Signature       string    `json:"signature,omitempty"`
}

type appsResponse struct {
	Version  string    `json:"version"`
	Platform string    `json:"platform"`
	Config   string    `json:"config"`
	BinDir   string    `json:"bin_dir"`
	Token    bool      `json:"token"`
	Apps     []appInfo `json:"apps"`
}

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	views, err := s.svc.Views()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	infos := make([]appInfo, 0, len(views))
	for _, v := range views {
		info := appInfo{
			ID: v.ID, Repo: v.Repo, Channel: v.Channel, Description: v.Description,
			Installed: v.Installed, CurrentVersion: v.CurrentVersion,
			LatestVersion: v.LatestVersion, LatestTag: v.LatestTag,
			Status: statusText(v), UpdateAvailable: v.UpdateAvailable && !v.Pinned,
			Comparable: v.Comparable, Pinned: v.Pinned, LinkPath: v.LinkPath,
			LastChecked: v.LastChecked, CheckError: v.CheckError,
		}
		if v.State != nil {
			info.Versions = v.State.Versions
		}
		if sig := v.Config.Signature; sig.Configured() {
			info.Signature = string(sig.NormalizedMode())
			if t := strings.TrimSpace(sig.Type); t != "" {
				info.Signature = t + "/" + info.Signature
			}
		}
		infos = append(infos, info)
	}
	writeJSON(w, http.StatusOK, appsResponse{
		Version: version.String(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Config: s.svc.Cfg.Path, BinDir: s.svc.BinDir,
		Token: s.svc.GH.HasToken(), Apps: infos,
	})
}

func statusText(v app.AppView) string {
	if !v.Installed {
		return "not installed"
	}
	if v.Pinned {
		return "pinned"
	}
	if v.CheckError != "" {
		return "check failed"
	}
	if v.UpdateAvailable {
		if !v.Comparable {
			return "different version"
		}
		return "update available"
	}
	if v.LatestVersion == "" {
		return "not checked"
	}
	return "up to date"
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Diagnostics())
}

// ---- job endpoints ----

type idsRequest struct {
	IDs []string `json:"ids"`
	All bool     `json:"all"`
}

type addRequest struct {
	Repo    string `json:"repo"`
	ID      string `json:"id"`
	Install bool   `json:"install"`
	Channel string `json:"channel"`
}

type rollbackRequest struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type pinRequest struct {
	ID     string `json:"id"`
	Pinned bool   `json:"pinned"`
}

type removeRequest struct {
	ID    string `json:"id"`
	Purge bool   `json:"purge"`
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	var req idsRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	j, err := s.jobs.start("check")
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	go func() {
		j.setStatus("running")
		results := s.svc.CheckFresh(s.context(), req.IDs, j.progress())
		j.finish(checkItems(results), nil)
	}()
	writeJSON(w, http.StatusAccepted, j.view(false))
}

func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	var req idsRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ids := req.IDs
	if len(ids) == 0 {
		if req.All {
			ids = s.svc.ConfiguredIDs()
		} else {
			ids = s.svc.MissingIDs()
		}
	}
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("nothing to install"))
		return
	}
	j, err := s.jobs.start("install")
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	go func() {
		j.setStatus("running")
		results := s.svc.InstallMany(s.context(), ids, app.InstallOptions{Progress: j.progress()})
		j.finish(installItems(results), nil)
	}()
	writeJSON(w, http.StatusAccepted, j.view(false))
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var req idsRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	j, err := s.jobs.start("update")
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	go func() {
		j.setStatus("running")
		ctx := s.context()
		ids := req.IDs
		var checks []app.CheckResult
		if len(ids) == 0 || req.All {
			checks = s.svc.CheckFresh(ctx, nil, j.progress())
			if len(ids) == 0 {
				for _, c := range checks {
					if c.Err == nil && c.UpdateAvailable && !c.Pinned {
						ids = append(ids, c.ID)
					}
				}
			}
		}
		if len(ids) == 0 {
			j.finish(updateResult{Checked: checkItems(checks), Installed: []installItem{}}, nil)
			return
		}
		results := s.svc.InstallMany(ctx, ids, app.InstallOptions{Progress: j.progress()})
		j.finish(updateResult{Checked: checkItems(checks), Installed: installItems(results)}, nil)
	}()
	writeJSON(w, http.StatusAccepted, j.view(false))
}

func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Repo) == "" {
		writeError(w, http.StatusBadRequest, errors.New("repo is required"))
		return
	}
	j, err := s.jobs.start("add")
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	go func() {
		j.setStatus("running")
		res, err := s.svc.Add(s.context(), app.AddOptions{
			Input: req.Repo, ID: req.ID, Channel: req.Channel,
			Install: req.Install, Progress: j.progress(),
		})
		if err != nil {
			j.finish(nil, err)
			return
		}
		out := addResult{ID: res.App.ID, Repo: res.App.Repo, Asset: res.Chosen.Name,
			Platforms: res.Platforms, Warnings: res.Warnings}
		if res.Installed != nil {
			installed := installItemFrom(*res.Installed)
			out.Installed = &installed
		}
		j.finish(out, nil)
	}()
	writeJSON(w, http.StatusAccepted, j.view(false))
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	var req rollbackRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.svc.Rollback(req.ID, req.Version)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, installItemFrom(res))
}

func (s *Server) handlePin(w http.ResponseWriter, r *http.Request) {
	var req pinRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.svc.SetPinned(req.ID, req.Pinned); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": req.ID, "pinned": req.Pinned})
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	var req removeRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, errors.New("id is required"))
		return
	}
	removed, err := s.svc.Remove(req.ID, req.Purge)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": req.ID, "removed": removed})
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("unknown job"))
		return
	}
	writeJSON(w, http.StatusOK, j.view(true))
}

func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("unknown job"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	send := func(v any) {
		data, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	snapshot := j.view(false)
	send(sseMessage{Type: "snapshot", Job: &snapshot})
	last := 0
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.context().Done():
			return
		case <-ticker.C:
			events, status, view := j.drain(last)
			for _, ev := range events {
				send(sseMessage{Type: "event", Event: &ev})
			}
			last += len(events)
			if status != "running" {
				send(sseMessage{Type: "done", Job: &view})
				return
			}
		}
	}
}

func (s *Server) context() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseCtx
}

// ---- JSON shapes for results ----

type checkItem struct {
	ID               string `json:"id"`
	InstalledVersion string `json:"installed_version,omitempty"`
	LatestVersion    string `json:"latest_version,omitempty"`
	LatestTag        string `json:"latest_tag,omitempty"`
	UpdateAvailable  bool   `json:"update_available"`
	Comparable       bool   `json:"comparable"`
	Pinned           bool   `json:"pinned"`
	Error            string `json:"error,omitempty"`
}

type installItem struct {
	ID       string   `json:"id"`
	Version  string   `json:"version"`
	Tag      string   `json:"tag,omitempty"`
	Previous string   `json:"previous,omitempty"`
	Asset    string   `json:"asset,omitempty"`
	SHA256   string   `json:"sha256,omitempty"`
	Link     string   `json:"link,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

type addResult struct {
	ID        string       `json:"id"`
	Repo      string       `json:"repo"`
	Asset     string       `json:"asset,omitempty"`
	Platforms []string     `json:"platforms,omitempty"`
	Warnings  []string     `json:"warnings,omitempty"`
	Installed *installItem `json:"installed,omitempty"`
}

type updateResult struct {
	Checked   []checkItem   `json:"checked"`
	Installed []installItem `json:"installed"`
}

func checkItems(results []app.CheckResult) []checkItem {
	out := make([]checkItem, len(results))
	for i, r := range results {
		out[i] = checkItem{
			ID: r.ID, InstalledVersion: r.InstalledVersion,
			LatestVersion: r.LatestVersion, LatestTag: r.LatestTag,
			UpdateAvailable: r.UpdateAvailable, Comparable: r.Comparable, Pinned: r.Pinned,
		}
		if r.Err != nil {
			out[i].Error = r.Err.Error()
		}
	}
	return out
}

func installItemFrom(r app.InstallResult) installItem {
	it := installItem{
		ID: r.ID, Version: r.Version, Tag: r.Tag, Previous: r.Previous,
		Asset: r.Asset, SHA256: r.SHA256, Link: r.Link, Warnings: r.Warnings,
	}
	if r.Err != nil {
		it.Error = r.Err.Error()
	}
	return it
}

func installItems(results []app.InstallResult) []installItem {
	out := make([]installItem, len(results))
	for i, r := range results {
		out[i] = installItemFrom(r)
	}
	return out
}

// ---- job manager ----

type Event struct {
	Time       time.Time `json:"time"`
	Kind       string    `json:"kind"`
	ID         string    `json:"id,omitempty"`
	Message    string    `json:"message,omitempty"`
	Percent    float64   `json:"percent,omitempty"`
	Speed      float64   `json:"speed,omitempty"`
	Downloaded int64     `json:"downloaded,omitempty"`
	Total      int64     `json:"total,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type jobView struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Status  string    `json:"status"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitempty"`
	Events  []Event   `json:"events,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   string    `json:"error,omitempty"`
}

type job struct {
	id     string
	kind   string
	status string
	start  time.Time
	end    time.Time
	result any
	err    error

	mu     sync.Mutex
	events []Event
}

func (j *job) setStatus(status string) {
	j.mu.Lock()
	j.status = status
	j.mu.Unlock()
}

func (j *job) add(ev Event) {
	ev.Time = time.Now()
	j.mu.Lock()
	j.events = append(j.events, ev)
	j.mu.Unlock()
}

func (j *job) progress() app.ProgressFunc {
	return func(ev app.Event) {
		out := Event{Kind: ev.Kind, ID: ev.ID, Message: ev.Message}
		if ev.Download.Total > 0 {
			out.Percent = ev.Download.Percent()
			out.Downloaded = ev.Download.Downloaded
			out.Total = ev.Download.Total
			out.Speed = ev.Download.BytesPerSec
		}
		if ev.Err != nil {
			out.Error = ev.Err.Error()
		}
		j.add(out)
	}
}

func (j *job) finish(result any, err error) {
	j.mu.Lock()
	j.result = result
	j.err = err
	j.end = time.Now()
	j.status = "done"
	if err != nil {
		j.status = "failed"
	}
	j.mu.Unlock()
}

func (j *job) view(withResult bool) jobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := jobView{
		ID: j.id, Kind: j.kind, Status: j.status, Started: j.start, Ended: j.end,
	}
	if withResult {
		v.Events = append([]Event{}, j.events...)
		v.Result = j.result
	}
	if j.err != nil {
		v.Error = j.err.Error()
	}
	return v
}

// drain returns events from offset, the current status, and a result view.
func (j *job) drain(offset int) ([]Event, string, jobView) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if offset > len(j.events) {
		offset = len(j.events)
	}
	events := append([]Event{}, j.events[offset:]...)
	v := jobView{ID: j.id, Kind: j.kind, Status: j.status, Started: j.start, Ended: j.end}
	if j.status != "running" {
		v.Result = j.result
		if j.err != nil {
			v.Error = j.err.Error()
		}
	}
	return events, j.status, v
}

type sseMessage struct {
	Type  string   `json:"type"`
	Event *Event   `json:"event,omitempty"`
	Job   *jobView `json:"job,omitempty"`
}

type jobManager struct {
	mu      sync.Mutex
	jobs    map[string]*job
	running string
	seq     int
}

func newJobManager() *jobManager {
	return &jobManager{jobs: map[string]*job{}}
}

// start creates a job, refusing while another one is still running.
func (m *jobManager) start(kind string) (*job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running != "" {
		if running, ok := m.jobs[m.running]; ok && running.status == "running" {
			return nil, fmt.Errorf("another operation (%s) is still running", running.kind)
		}
	}
	m.seq++
	j := &job{id: fmt.Sprintf("%s-%d-%d", kind, time.Now().UnixNano(), m.seq), kind: kind, status: "running", start: time.Now()}
	m.jobs[j.id] = j
	m.running = j.id
	m.gc()
	return j, nil
}

// gc keeps the most recent 40 finished jobs.
func (m *jobManager) gc() {
	if len(m.jobs) <= 40 {
		return
	}
	var finished []*job
	for _, j := range m.jobs {
		j.mu.Lock()
		if j.status != "running" {
			finished = append(finished, j)
		}
		j.mu.Unlock()
	}
	sort.Slice(finished, func(i, k int) bool { return finished[i].start.Before(finished[k].start) })
	for _, j := range finished {
		if len(m.jobs) <= 40 {
			break
		}
		delete(m.jobs, j.id)
	}
}

func (m *jobManager) get(id string) (*job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}
