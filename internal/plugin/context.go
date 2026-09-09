package plugin

import (
	"context"
	"errors"
	"fmt"

	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/platform/process"
	"github.com/inipew/goultroid/internal/platform/secret"
	"github.com/inipew/goultroid/internal/tasks"
)

// PluginContext provides capability-gated access to runtime platform services.
type PluginContext interface {
	context.Context
	Scope() *Scope
	Owner() string

	// Capability-gated service accessors
	HTTP() (*network.Service, error)
	Process() (*process.Manager, error)
	Files() (*filesystem.Manager, error)
	Secrets() (*secret.Manager, error)
	Tasks() (*tasks.Manager, error)
	Jobs() (*jobs.Manager, error)
}

type pluginContext struct {
	context.Context
	scope   *Scope
	owner   string
	gate    *CapabilityGate
	network *network.Service
	process *process.Manager
	files   *filesystem.Manager
	secrets *secret.Manager
	tasks   *tasks.Manager
	jobs    *jobs.Manager
}

// ContextConfig bundles runtime services provided to a plugin context.
type ContextConfig struct {
	Scope   *Scope
	Owner   string
	Gate    *CapabilityGate
	Network *network.Service
	Process *process.Manager
	Files   *filesystem.Manager
	Secrets *secret.Manager
	Tasks   *tasks.Manager
	Jobs    *jobs.Manager
}

// NewPluginContext constructs a new PluginContext enforcing capability checks via the gate.
func NewPluginContext(baseCtx context.Context, cfg ContextConfig) PluginContext {
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	if cfg.Scope != nil {
		baseCtx = cfg.Scope.Context()
	}
	if cfg.Gate == nil {
		cfg.Gate = NewCapabilityGate()
	}

	return &pluginContext{
		Context: baseCtx,
		scope:   cfg.Scope,
		owner:   cfg.Owner,
		gate:    cfg.Gate,
		network: cfg.Network,
		process: cfg.Process,
		files:   cfg.Files,
		secrets: cfg.Secrets,
		tasks:   cfg.Tasks,
		jobs:    cfg.Jobs,
	}
}

func (c *pluginContext) Scope() *Scope {
	return c.scope
}

func (c *pluginContext) Owner() string {
	return c.owner
}

func (c *pluginContext) HTTP() (*network.Service, error) {
	if err := c.gate.Check(c.owner, CapHTTP); err != nil {
		return nil, fmt.Errorf("http access denied: %w", err)
	}
	if c.network == nil {
		return nil, errors.New("network service not configured")
	}
	return c.network, nil
}

func (c *pluginContext) Process() (*process.Manager, error) {
	if err := c.gate.Check(c.owner, CapProcessExecute); err != nil {
		return nil, fmt.Errorf("process execution denied: %w", err)
	}
	if c.process == nil {
		return nil, errors.New("process manager not configured")
	}
	return c.process, nil
}

func (c *pluginContext) Files() (*filesystem.Manager, error) {
	if err := c.gate.Check(c.owner, CapFilesystemData); err != nil {
		if errTemp := c.gate.Check(c.owner, CapFilesystemTemp); errTemp != nil {
			return nil, fmt.Errorf("filesystem access denied: requires %s or %s: %w", CapFilesystemData, CapFilesystemTemp, err)
		}
	}
	if c.files == nil {
		return nil, errors.New("filesystem manager not configured")
	}
	return c.files, nil
}

func (c *pluginContext) Secrets() (*secret.Manager, error) {
	if err := c.gate.Check(c.owner, CapSecretRead); err != nil {
		return nil, fmt.Errorf("secret access denied: %w", err)
	}
	if c.secrets == nil {
		return nil, errors.New("secret manager not configured")
	}
	return c.secrets, nil
}

func (c *pluginContext) Tasks() (*tasks.Manager, error) {
	if err := c.gate.Check(c.owner, CapTasks); err != nil {
		return nil, fmt.Errorf("task access denied: %w", err)
	}
	if c.tasks == nil {
		return nil, errors.New("task manager not configured")
	}
	return c.tasks, nil
}

func (c *pluginContext) Jobs() (*jobs.Manager, error) {
	if err := c.gate.Check(c.owner, CapJobs); err != nil {
		if errSched := c.gate.Check(c.owner, CapScheduler); errSched != nil {
			return nil, fmt.Errorf("jobs access denied: requires %s or %s: %w", CapJobs, CapScheduler, err)
		}
	}
	if c.jobs == nil {
		return nil, errors.New("jobs manager not configured")
	}
	return c.jobs, nil
}
