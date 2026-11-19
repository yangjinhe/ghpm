package main

import (
	"context"

	"ghpm/internal/app"
)

// backend is the subset of app.Service the native UI drives. Keeping it an
// interface lets the view tests run without touching the network or the
// user's state database.
type backend interface {
	Views() ([]app.AppView, error)
	CheckFresh(ctx context.Context, ids []string, progress app.ProgressFunc) []app.CheckResult
	InstallOne(ctx context.Context, id string, opts app.InstallOptions) app.InstallResult
	InstallMany(ctx context.Context, ids []string, opts app.InstallOptions) []app.InstallResult
	Add(ctx context.Context, opts app.AddOptions) (*app.AddResult, error)
	Rollback(id, version string) (app.InstallResult, error)
	SetPinned(id string, pinned bool) error
	Remove(id string, purge bool) ([]string, error)
	ConfigPath() string
	DataDir() string
	BinDir() string
	AppsDir() string
	HasToken() bool
}

type serviceBackend struct{ svc *app.Service }

func (b serviceBackend) Views() ([]app.AppView, error) { return b.svc.Views() }

func (b serviceBackend) CheckFresh(ctx context.Context, ids []string, progress app.ProgressFunc) []app.CheckResult {
	return b.svc.CheckFresh(ctx, ids, progress)
}

func (b serviceBackend) InstallOne(ctx context.Context, id string, opts app.InstallOptions) app.InstallResult {
	return b.svc.InstallOne(ctx, id, opts)
}

func (b serviceBackend) InstallMany(ctx context.Context, ids []string, opts app.InstallOptions) []app.InstallResult {
	return b.svc.InstallMany(ctx, ids, opts)
}

func (b serviceBackend) Add(ctx context.Context, opts app.AddOptions) (*app.AddResult, error) {
	return b.svc.Add(ctx, opts)
}

func (b serviceBackend) Rollback(id, version string) (app.InstallResult, error) {
	return b.svc.Rollback(id, version)
}

func (b serviceBackend) SetPinned(id string, pinned bool) error {
	return b.svc.SetPinned(id, pinned)
}

func (b serviceBackend) Remove(id string, purge bool) ([]string, error) {
	return b.svc.Remove(id, purge)
}

func (b serviceBackend) ConfigPath() string { return b.svc.Cfg.Path }
func (b serviceBackend) DataDir() string    { return b.svc.DataDir }
func (b serviceBackend) BinDir() string     { return b.svc.BinDir }
func (b serviceBackend) AppsDir() string    { return b.svc.AppsDir }
func (b serviceBackend) HasToken() bool     { return b.svc.GH.HasToken() }
