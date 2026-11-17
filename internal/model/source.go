package model

import "time"

// Source origins.
const (
	// SourceOriginConfig marks a source declared in config.yaml (read-only in the UI).
	SourceOriginConfig = "config"
	// SourceOriginUser marks a source created through the API / UI and stored in the database.
	SourceOriginUser = "user"
)

// Source types.
const (
	SourceTypeKubernetes = "kubernetes"
)

// SourceSpec fully describes a collector source, wherever it was declared.
type SourceSpec struct {
	Name     string
	Type     string
	Origin   string
	Enabled  bool
	ReadOnly bool

	// Kubernetes
	KubeconfigPath string
	KubeContext    string
	// Kubeconfig holds uploaded kubeconfig content (decrypted). Never serialised.
	Kubeconfig []byte
	// KubeconfigEnc is the encrypted form persisted in the database.
	KubeconfigEnc []byte

	// PollInterval overrides the global sampling interval when > 0.
	PollInterval time.Duration

	CreatedAt time.Time
	UpdatedAt time.Time
}
