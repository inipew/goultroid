// Package plugin provides capability-gated plugin lifecycle and runtime access.
//
// PluginContext exposes consumer-sized service capabilities. Jobs and schedules
// are reached through scoped adapters that stamp plugin ownership rather than
// exposing the concrete jobs.Manager, and task access is similarly fenced to
// the plugin generation scope.
package plugin
