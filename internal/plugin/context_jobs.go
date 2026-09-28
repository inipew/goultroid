package plugin

import (
	"context"
	"strings"

	"github.com/inipew/goultroid/internal/jobs"
)

// JobClient is the plugin-owned jobs capability. It deliberately omits
// lifecycle, recovery, diagnostics, raw occurrence mutation, and scheduling.
type JobClient interface {
	RegisterHandler(handlerType string, handler jobs.Handler) error
	Register(def jobs.JobDefinition) error
	Trigger(context.Context, string) error
}

// ScheduleClient is the plugin-owned scheduling capability. It cannot invoke
// job recovery, lifecycle, retry, diagnostics, or due-processing operations.
type ScheduleClient interface {
	SaveSchedule(context.Context, jobs.JobSchedule) error
	DisableSchedule(context.Context, string) error
}

type scopedJobBackend interface {
	RegisterHandler(string, jobs.Handler) error
	Register(jobs.JobDefinition) error
	Trigger(context.Context, string) error
}

type scopedScheduleBackend interface {
	SaveSchedule(context.Context, jobs.JobSchedule) error
	DisableSchedule(context.Context, string) error
}

type scopedJobClient struct {
	manager scopedJobBackend
	owner   string
}

type scopedScheduleClient struct {
	manager scopedScheduleBackend
	owner   string
}

func encodePluginJobComponent(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "%", "%25")
	return strings.ReplaceAll(value, ":", "%3A")
}

func pluginJobOwner(owner string) string {
	return "plugin:" + encodePluginJobComponent(owner)
}

func pluginJobOwnerCleanupAliases(owner string) []string {
	owner = strings.TrimSpace(owner)
	aliases := []string{owner}
	legacy := "plugin:" + owner
	aliases = append(aliases, legacy)
	encoded := pluginJobOwner(owner)
	if encoded != legacy {
		aliases = append(aliases, encoded)
	}
	return aliases
}

func pluginScopedName(owner, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return pluginJobOwner(owner) + ":" + encodePluginJobComponent(name)
}

func (c scopedJobClient) RegisterHandler(handlerType string, handler jobs.Handler) error {
	return c.manager.RegisterHandler(pluginScopedName(c.owner, handlerType), handler)
}

func (c scopedJobClient) Register(def jobs.JobDefinition) error {
	def.ID = pluginScopedName(c.owner, def.ID)
	def.ScopeOwner = pluginJobOwner(c.owner)
	def.QuotaOwner = pluginJobOwner(c.owner)
	def.HandlerType = pluginScopedName(c.owner, def.HandlerType)
	return c.manager.Register(def)
}

func (c scopedJobClient) Trigger(ctx context.Context, jobID string) error {
	return c.manager.Trigger(ctx, pluginScopedName(c.owner, jobID))
}

func (c scopedScheduleClient) SaveSchedule(ctx context.Context, schedule jobs.JobSchedule) error {
	schedule.ID = pluginScopedName(c.owner, schedule.ID)
	schedule.JobID = pluginScopedName(c.owner, schedule.JobID)
	return c.manager.SaveSchedule(ctx, schedule)
}

func (c scopedScheduleClient) DisableSchedule(ctx context.Context, scheduleID string) error {
	return c.manager.DisableSchedule(ctx, pluginScopedName(c.owner, scheduleID))
}
