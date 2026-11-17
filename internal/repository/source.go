package repository

import (
	"context"
	"errors"

	"github.com/emirhanusta/kymo/internal/model"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a record with the same key already exists.
var ErrConflict = errors.New("already exists")

// SourceRepository persists user-created collector sources. KubeconfigEnc is
// stored as-is; encryption happens in the service layer.
type SourceRepository interface {
	List(ctx context.Context) ([]*model.SourceSpec, error)
	Get(ctx context.Context, name string) (*model.SourceSpec, error)
	// Create returns ErrConflict when the name is taken.
	Create(ctx context.Context, s *model.SourceSpec) error
	// Update replaces every field except name, type and created_at.
	// Returns ErrNotFound when the source does not exist.
	Update(ctx context.Context, s *model.SourceSpec) error
	// Delete returns ErrNotFound when the source does not exist.
	Delete(ctx context.Context, name string) error
}
