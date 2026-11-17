package service

import (
	"context"
	"errors"
	"sort"
	"time"

	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
)

// ErrNoConfigFile is returned by ReloadConfig when no config file is in use.
var ErrNoConfigFile = errors.New("no config file in use")

// ConfigError wraps a failure to read or validate the config file.
type ConfigError struct{ Err error }

func (e *ConfigError) Error() string { return e.Err.Error() }
func (e *ConfigError) Unwrap() error { return e.Err }

// SyncResult describes what a config sync changed.
type SyncResult struct {
	Added      []string  `json:"added"`
	Removed    []string  `json:"removed"`
	Updated    []string  `json:"updated"`
	Unchanged  []string  `json:"unchanged"`
	Conflicts  []string  `json:"conflicts"`
	ReloadedAt time.Time `json:"reloaded_at"`
}

// ConfigSync is the status of the last automatic or manual config sync.
type ConfigSync struct {
	ReloadedAt time.Time   `json:"reloaded_at"`
	Result     *SyncResult `json:"result"`
	Error      string      `json:"error,omitempty"`
}

// SetConfigReloader tells the manager which config file is in use and how to
// re-read the config sources from it.
func (m *SourceManager) SetConfigReloader(file string, load func() ([]model.SourceSpec, error)) {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	m.configFile, m.loadSpecs = file, load
}

// ConfigFile returns the config file path in use, or "".
func (m *SourceManager) ConfigFile() string {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	return m.configFile
}

// LastConfigSync returns the last sync status, or nil if none happened yet.
func (m *SourceManager) LastConfigSync() *ConfigSync {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	if m.lastSync == nil {
		return nil
	}
	c := *m.lastSync
	return &c
}

// ReloadConfig re-reads the config file and applies its sources. A read or
// validation failure leaves the running sources untouched, is recorded as the
// last sync error and returned as a *ConfigError.
func (m *SourceManager) ReloadConfig(ctx context.Context) (SyncResult, error) {
	m.syncMu.Lock()
	load := m.loadSpecs
	m.syncMu.Unlock()
	if load == nil {
		return SyncResult{}, ErrNoConfigFile
	}
	specs, err := load()
	if err != nil {
		m.syncMu.Lock()
		m.lastSync = &ConfigSync{ReloadedAt: time.Now().UTC(), Error: err.Error()}
		m.syncMu.Unlock()
		return SyncResult{}, &ConfigError{Err: err}
	}
	return m.SyncConfig(ctx, specs)
}

func sameSettings(a, b model.SourceSpec) bool {
	return a.Type == b.Type && a.KubeconfigPath == b.KubeconfigPath &&
		a.KubeContext == b.KubeContext && a.PollInterval == b.PollInterval &&
		a.ReadOnly == b.ReadOnly && a.Enabled == b.Enabled
}

// SyncConfig applies the diff between the config-origin sources currently
// running and specs: new ones are attached, vanished ones detached (history is
// kept), changed ones re-attached and unchanged ones left running. A config
// name that collides with a user source is reported as a conflict and the user
// source is left alone.
func (m *SourceManager) SyncConfig(ctx context.Context, specs []model.SourceSpec) (SyncResult, error) {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	res := SyncResult{Added: []string{}, Removed: []string{}, Updated: []string{},
		Unchanged: []string{}, Conflicts: []string{}, ReloadedAt: time.Now().UTC()}
	if m.configSpecs == nil {
		m.configSpecs = map[string]model.SourceSpec{}
	}

	next := make(map[string]model.SourceSpec, len(specs))
	for _, spec := range specs {
		spec.Origin = model.SourceOriginConfig
		old, applied := m.configSpecs[spec.Name]
		if !applied {
			if existing, ok := m.recorder.Source(spec.Name); ok && existing.Origin != model.SourceOriginConfig {
				m.log.Warn("config.yaml source collides with a user source; keeping the user source",
					zap.String("source", spec.Name))
				res.Conflicts = append(res.Conflicts, spec.Name)
				continue
			}
		}
		next[spec.Name] = spec
		switch {
		case !applied:
			m.attach(spec, nil)
			res.Added = append(res.Added, spec.Name)
		case !sameSettings(old, spec):
			m.attach(spec, nil)
			res.Updated = append(res.Updated, spec.Name)
		default:
			res.Unchanged = append(res.Unchanged, spec.Name)
		}
	}
	for name := range m.configSpecs {
		if _, keep := next[name]; keep {
			continue
		}
		m.recorder.DetachSource(name)
		m.mgmt.UnregisterSource(name)
		res.Removed = append(res.Removed, name)
	}
	m.configSpecs = next

	for _, l := range [][]string{res.Added, res.Removed, res.Updated, res.Unchanged, res.Conflicts} {
		sort.Strings(l)
	}
	r := res
	m.lastSync = &ConfigSync{ReloadedAt: res.ReloadedAt, Result: &r}
	return res, nil
}
