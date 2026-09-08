package module

import (
	"context"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
)

type Manifest struct {
	ID           string
	Version      string
	Description  string
	Dependencies []string
}

type Runtime struct {
	DB      *database.DB
	OwnerID int64
	Plugins *plugin.Manager
}

type Module interface {
	Manifest() Manifest
	Register(context.Context, *Runtime) error
}

func Validate(m Module) error {
	if m == nil {
		return ErrNilModule
	}
	manifest := m.Manifest()
	if manifest.ID == "" {
		return ErrEmptyModuleID
	}
	if manifest.Version == "" {
		return ErrEmptyModuleVersion
	}
	return nil
}
