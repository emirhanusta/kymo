package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	containertypes "github.com/docker/docker/api/types/container"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/emirhanusta/kymo/internal/collector"
	"github.com/emirhanusta/kymo/internal/kube"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/secret"
)

// Errors returned by SourceManager. Handlers map them to HTTP status codes.
var (
	// ErrSourceLocked is returned when trying to change a config.yaml source.
	ErrSourceLocked = errors.New("source is defined in config.yaml and cannot be changed from the API")
	// ErrSourceNotFound is returned for unknown source names.
	ErrSourceNotFound = errors.New("source not found")
	// ErrSourceExists is returned when a source name is already taken.
	ErrSourceExists = errors.New("a source with this name already exists")
)

// ValidationError describes invalid source input.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// SourceInput is the create/update payload for a source.
type SourceInput struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Enabled  *bool  `json:"enabled,omitempty"`
	ReadOnly bool   `json:"read_only"`

	Socket string `json:"socket,omitempty"`

	KubeconfigPath string `json:"kubeconfig_path,omitempty"`
	// Kubeconfig is raw kubeconfig YAML. On update, empty keeps the stored
	// upload unless KubeconfigPath is set (switching to a server-side file).
	Kubeconfig  string `json:"kubeconfig,omitempty"`
	KubeContext string `json:"kube_context,omitempty"`

	PollIntervalSeconds int `json:"poll_interval_seconds,omitempty"`
}

// SourceCheck is one line of a connection test.
type SourceCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Warning bool   `json:"warning,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// SourceTestResult is the outcome of a connection test. OK is true when every
// check passed (warnings do not fail the test).
type SourceTestResult struct {
	OK     bool          `json:"ok"`
	Checks []SourceCheck `json:"checks"`
}

// CollectorFactory builds the collector for a source spec.
type CollectorFactory func(spec model.SourceSpec) collector.Collector

// SourceManager owns the lifecycle of collector sources: it loads config and
// database sources at startup and creates, updates and deletes user sources
// at runtime, keeping the recorder and the management clients in sync.
type SourceManager struct {
	repo     repository.SourceRepository
	box      *secret.Box
	recorder RecorderService
	mgmt     ManagementService
	factory  CollectorFactory
	log      *zap.Logger
}

// NewSourceManager wires a SourceManager.
func NewSourceManager(repo repository.SourceRepository, box *secret.Box, recorder RecorderService,
	mgmt ManagementService, factory CollectorFactory, log *zap.Logger) *SourceManager {
	return &SourceManager{repo: repo, box: box, recorder: recorder, mgmt: mgmt, factory: factory, log: log}
}

// SecretConfigured reports whether uploaded kubeconfigs can be stored.
func (m *SourceManager) SecretConfigured() bool { return m.box.Configured() }

// Bootstrap attaches config.yaml sources, then database sources. A database
// source whose name collides with a config source is skipped.
func (m *SourceManager) Bootstrap(ctx context.Context, configSpecs []model.SourceSpec) error {
	for _, spec := range configSpecs {
		spec.Origin = model.SourceOriginConfig
		m.attach(spec, nil)
	}
	stored, err := m.repo.List(ctx)
	if err != nil {
		return err
	}
	for _, spec := range stored {
		if existing, ok := m.recorder.Source(spec.Name); ok && existing.Origin == model.SourceOriginConfig {
			m.log.Warn("database source shadowed by config.yaml source; skipping", zap.String("source", spec.Name))
			continue
		}
		var initErr error
		if len(spec.KubeconfigEnc) > 0 {
			spec.Kubeconfig, initErr = m.box.Open(spec.KubeconfigEnc)
		}
		m.attach(*spec, initErr)
	}
	return nil
}

// attach registers a spec with the recorder and the management service.
func (m *SourceManager) attach(spec model.SourceSpec, initErr error) {
	if initErr != nil {
		m.mgmt.UnregisterSource(spec.Name)
		m.recorder.AttachSource(spec, nil, initErr)
		return
	}
	if spec.Enabled {
		if err := m.mgmt.RegisterSource(spec); err != nil {
			m.log.Warn("management client unavailable", zap.String("source", spec.Name), zap.Error(err))
		}
	} else {
		m.mgmt.UnregisterSource(spec.Name)
	}
	m.recorder.AttachSource(spec, m.factory(spec), nil)
}

var sourceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// buildSpec validates input and turns it into a spec (with Kubeconfig set when
// uploaded content is provided). existing is the stored spec on update.
func (m *SourceManager) buildSpec(in SourceInput, existing *model.SourceSpec) (model.SourceSpec, error) {
	spec := model.SourceSpec{
		Name:     strings.TrimSpace(in.Name),
		Type:     in.Type,
		Origin:   model.SourceOriginUser,
		Enabled:  in.Enabled == nil || *in.Enabled,
		ReadOnly: in.ReadOnly,
	}
	if existing != nil {
		spec.Name, spec.Type, spec.CreatedAt = existing.Name, existing.Type, existing.CreatedAt
		if in.Type != "" && in.Type != existing.Type {
			return spec, invalid("the type of an existing source cannot be changed")
		}
	}
	if !sourceNameRE.MatchString(spec.Name) {
		return spec, invalid("name must be 1–63 characters: lowercase letters, digits, '.', '_' or '-', starting with a letter or digit")
	}
	if in.PollIntervalSeconds != 0 && (in.PollIntervalSeconds < 5 || in.PollIntervalSeconds > 3600) {
		return spec, invalid("poll interval must be between 5 and 3600 seconds")
	}
	spec.PollInterval = time.Duration(in.PollIntervalSeconds) * time.Second

	switch spec.Type {
	case model.SourceTypeDocker:
		spec.Socket = strings.TrimSpace(in.Socket)
	case model.SourceTypeKubernetes:
		spec.KubeconfigPath = strings.TrimSpace(in.KubeconfigPath)
		spec.KubeContext = strings.TrimSpace(in.KubeContext)
		switch {
		case strings.TrimSpace(in.Kubeconfig) != "":
			if !m.box.Configured() {
				return spec, invalid("%s", secret.ErrNoKey.Error())
			}
			if _, err := clientcmd.Load([]byte(in.Kubeconfig)); err != nil {
				return spec, invalid("kubeconfig is not valid: %v", err)
			}
			spec.Kubeconfig = []byte(in.Kubeconfig)
			spec.KubeconfigPath = "" // uploaded content wins
		case spec.KubeconfigPath == "" && existing != nil && len(existing.Kubeconfig) > 0:
			spec.Kubeconfig = existing.Kubeconfig // keep the stored upload
		}
	default:
		return spec, invalid("type must be \"docker\" or \"kubernetes\"")
	}
	return spec, nil
}

// seal encrypts uploaded kubeconfig content for storage.
func (m *SourceManager) seal(spec *model.SourceSpec) error {
	spec.KubeconfigEnc = nil
	if len(spec.Kubeconfig) == 0 {
		return nil
	}
	enc, err := m.box.Seal(spec.Kubeconfig)
	if err != nil {
		return err
	}
	spec.KubeconfigEnc = enc
	return nil
}

// Create validates, stores and starts a new user source.
func (m *SourceManager) Create(ctx context.Context, in SourceInput) (model.SourceSpec, error) {
	spec, err := m.buildSpec(in, nil)
	if err != nil {
		return spec, err
	}
	if _, taken := m.recorder.Source(spec.Name); taken {
		return spec, ErrSourceExists
	}
	if err := m.seal(&spec); err != nil {
		return spec, err
	}
	if err := m.repo.Create(ctx, &spec); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return spec, ErrSourceExists
		}
		return spec, err
	}
	m.attach(spec, nil)
	return spec, nil
}

// userSource returns the registered spec for name if it is a user source.
func (m *SourceManager) userSource(name string) (model.SourceSpec, error) {
	spec, ok := m.recorder.Source(name)
	if !ok {
		return spec, ErrSourceNotFound
	}
	if spec.Origin != model.SourceOriginUser {
		return spec, ErrSourceLocked
	}
	return spec, nil
}

// Update replaces a user source's settings and restarts its collector.
func (m *SourceManager) Update(ctx context.Context, name string, in SourceInput) (model.SourceSpec, error) {
	existing, err := m.userSource(name)
	if err != nil {
		return existing, err
	}
	spec, err := m.buildSpec(in, &existing)
	if err != nil {
		return spec, err
	}
	if err := m.seal(&spec); err != nil {
		return spec, err
	}
	if err := m.repo.Update(ctx, &spec); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return spec, ErrSourceNotFound
		}
		return spec, err
	}
	m.attach(spec, nil)
	return spec, nil
}

// Delete stops and removes a user source; with purge it also deletes its history.
func (m *SourceManager) Delete(ctx context.Context, name string, purge bool) error {
	if _, err := m.userSource(name); err != nil {
		return err
	}
	if err := m.repo.Delete(ctx, name); err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	m.recorder.DetachSource(name)
	m.mgmt.UnregisterSource(name)
	if purge {
		return m.recorder.PurgeSource(ctx, name)
	}
	return nil
}

// Contexts lists kubeconfig contexts from a server-side path or raw content.
func (m *SourceManager) Contexts(path, content string) ([]kube.Context, string, error) {
	return kube.Contexts(strings.TrimSpace(path), []byte(content))
}

// Test checks that a source can be reached without saving it. When editing an
// existing source with an uploaded kubeconfig and no new content or path is
// given, the stored kubeconfig is used.
func (m *SourceManager) Test(ctx context.Context, in SourceInput) SourceTestResult {
	var existing *model.SourceSpec
	if spec, ok := m.recorder.Source(strings.TrimSpace(in.Name)); ok && spec.Origin == model.SourceOriginUser {
		existing = &spec
		if in.Type == "" {
			in.Type = spec.Type
		}
	}
	spec, err := m.buildSpec(in, existing)
	if err != nil {
		// Name validation is not the test's concern when no name was given yet.
		var ve *ValidationError
		if errors.As(err, &ve) && strings.TrimSpace(in.Name) == "" {
			in.Name = "test"
			spec, err = m.buildSpec(in, nil)
		}
		if err != nil {
			return SourceTestResult{Checks: []SourceCheck{{Name: "Settings", Detail: err.Error()}}}
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var checks []SourceCheck
	if spec.Type == model.SourceTypeDocker {
		checks = testDocker(ctx, spec)
	} else {
		checks = testKubernetes(ctx, spec)
	}
	ok := true
	for _, c := range checks {
		if !c.OK {
			ok = false
		}
	}
	return SourceTestResult{OK: ok, Checks: checks}
}

func testDocker(ctx context.Context, spec model.SourceSpec) []SourceCheck {
	cli, err := NewDockerClient(spec.Socket)
	if err != nil {
		return []SourceCheck{{Name: "Docker client", Detail: err.Error()}}
	}
	defer cli.Close()

	if _, err := cli.Ping(ctx); err != nil {
		return []SourceCheck{{Name: "Daemon reachable", Detail: err.Error()}}
	}
	checks := []SourceCheck{}
	if v, err := cli.ServerVersion(ctx); err == nil {
		checks = append(checks, SourceCheck{Name: "Daemon reachable", OK: true,
			Detail: fmt.Sprintf("Docker %s (API %s, %s/%s)", v.Version, v.APIVersion, v.Os, v.Arch)})
	} else {
		checks = append(checks, SourceCheck{Name: "Daemon reachable", OK: true})
	}
	list, err := cli.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return append(checks, SourceCheck{Name: "List containers", Detail: err.Error()})
	}
	running := 0
	for _, c := range list {
		if c.State == "running" {
			running++
		}
	}
	return append(checks, SourceCheck{Name: "List containers", OK: true,
		Detail: fmt.Sprintf("%d containers, %d running", len(list), running)})
}

func testKubernetes(ctx context.Context, spec model.SourceSpec) []SourceCheck {
	cfg, err := kube.RestConfig(spec.KubeconfigPath, spec.Kubeconfig, spec.KubeContext)
	if err != nil {
		return []SourceCheck{{Name: "Kubeconfig", Detail: err.Error()}}
	}
	cfg.Timeout = 10 * time.Second

	ctxName := spec.KubeContext
	if ctxName == "" {
		if _, current, err := kube.Contexts(spec.KubeconfigPath, spec.Kubeconfig); err == nil {
			ctxName = current
		}
	}
	checks := []SourceCheck{{Name: "Kubeconfig", OK: true, Detail: fmt.Sprintf("context %q → %s", ctxName, cfg.Host)}}

	if !spec.ReadOnly && looksLikeProduction(ctxName, cfg.Host) {
		checks = append(checks, SourceCheck{Name: "Production guard", OK: true, Warning: true,
			Detail: "this looks like a production cluster; consider making the source read-only"})
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return append(checks, SourceCheck{Name: "API server", Detail: err.Error()})
	}
	v, err := cs.Discovery().ServerVersion()
	if err != nil {
		return append(checks, SourceCheck{Name: "API server", Detail: err.Error()})
	}
	checks = append(checks, SourceCheck{Name: "API server", OK: true, Detail: "Kubernetes " + v.GitVersion})

	if ns, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{}); err != nil {
		checks = append(checks, SourceCheck{Name: "Permissions", Detail: "cannot list namespaces: " + err.Error()})
	} else {
		checks = append(checks, SourceCheck{Name: "Permissions", OK: true, Detail: fmt.Sprintf("%d namespaces visible", len(ns.Items))})
	}
	if _, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
		checks = append(checks, SourceCheck{Name: "Watch pods", Detail: "cannot list pods: " + err.Error()})
	} else {
		checks = append(checks, SourceCheck{Name: "Watch pods", OK: true})
	}

	if _, err := cs.Discovery().ServerResourcesForGroupVersion("metrics.k8s.io/v1beta1"); err != nil {
		checks = append(checks, SourceCheck{Name: "metrics-server", OK: true, Warning: true,
			Detail: "not installed; CPU and memory will not be recorded"})
	} else {
		checks = append(checks, SourceCheck{Name: "metrics-server", OK: true, Detail: "available"})
	}
	return checks
}

// looksLikeProduction flags context names or API hosts that mention prod.
func looksLikeProduction(values ...string) bool {
	for _, v := range values {
		if strings.Contains(strings.ToLower(v), "prod") {
			return true
		}
	}
	return false
}
