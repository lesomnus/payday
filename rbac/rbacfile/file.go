// Package rbacfile provides read-only YAML method policies. No database rows
// are provisioned: a successful refresh replaces the source's entire policy.
package rbacfile

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/rbac"
	"github.com/lesomnus/xli/cfg"
)

type Config struct {
	File    string        `yaml:"file"`
	Refresh time.Duration `yaml:"refresh"`
}

type File struct {
	*rbac.Store
	file      *cfg.File[rbac.Rules]
	every     time.Duration
	refreshMu sync.Mutex
}

func (f *File) SpinName() string { return "rbac:" + f.file.Path() }
func (f *File) SpinEverywhere()  {}

// Open validates the first snapshot before anything is served. Later reads
// use cfg's stable-file reload and retain the last valid snapshot on errors.
func Open(c Config, checks ...func(rbac.Rules) error) (*File, error) {
	if c.File == "" {
		return nil, fmt.Errorf("policy.file: no file")
	}
	f := cfg.NewFile[rbac.Rules](c.File)
	v, err := f.Load()
	if err != nil {
		return nil, err
	}
	s, err := rbac.NewStore(*v.Config, checks...)
	if err != nil {
		return nil, err
	}
	if c.Refresh <= 0 {
		c.Refresh = 5 * time.Second
	}
	return &File{Store: s, file: f, every: c.Refresh}, nil
}

func (f *File) Refresh() error {
	f.refreshMu.Lock()
	defer f.refreshMu.Unlock()
	v, _, err := f.file.Reload()
	if err != nil {
		return err
	}
	return f.Replace(*v.Config)
}

func (f *File) Grants(ctx context.Context, name string) ([]rbac.Grant, error) {
	gs, err := f.Store.Grants(ctx, name)
	for i := range gs {
		gs[i].Source = f.file.Path()
	}
	return gs, err
}

func (f *File) Spin(ctx context.Context) error {
	t := time.NewTicker(f.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := f.Refresh(); err != nil {
				log.From(ctx).WarnContext(ctx, "authorization policy reload failed; keeping previous policy", slog.String("file", f.file.Path()), slog.Any("error", err))
			}
		}
	}
}
