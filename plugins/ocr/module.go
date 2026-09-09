package ocr

import (
	"context"

	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/plugin"
)

type ModuleType struct{}

var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID:           "ocr",
		Version:      "1.1.0",
		Description:  "Extract text from a replied Telegram image using OCR.Space",
		Capabilities: []string{plugin.CapHTTP, plugin.CapFilesystemTemp, plugin.CapSecretRead},
	}
}

func (m ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	return rt.RegisterPlugin(ctx, m.Manifest(), New())
}

var _ module.Module = ModuleType{}
