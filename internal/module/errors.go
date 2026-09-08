package module

import "errors"

var (
	ErrNilModule          = errors.New("module is nil")
	ErrEmptyModuleID      = errors.New("module ID is empty")
	ErrEmptyModuleVersion = errors.New("module version is empty")
	ErrNilRuntime         = errors.New("module runtime is nil")
	ErrNilDatabase        = errors.New("module runtime database is nil")
	ErrNilPluginManager   = errors.New("module runtime plugin manager is nil")
)
