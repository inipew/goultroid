package plugin

import "strings"

// Group represents a logical domain category for plugins.
type Group string

const (
	GroupCore         Group = "Core"
	GroupAdmin        Group = "Admin"
	GroupModeration   Group = "Moderation"
	GroupMedia        Group = "Media"
	GroupProductivity Group = "Productivity"
	GroupInline       Group = "Inline"
	GroupFun          Group = "Fun"
	GroupSystem       Group = "System"
	GroupVoice        Group = "Voice"
	GroupAddon        Group = "Addon"
)

// DefaultPluginGroups maps canonical plugin identifiers to their logical domain group.
var DefaultPluginGroups = map[string]Group{
	"help":       GroupCore,
	"info":       GroupCore,
	"pin":        GroupCore,
	"forward":    GroupCore,
	"profile":    GroupCore,
	"admin":      GroupAdmin,
	"sudo":       GroupAdmin,
	"pmpermit":   GroupAdmin,
	"userlog":    GroupAdmin,
	"locks":      GroupModeration,
	"blacklist":  GroupModeration,
	"media":      GroupMedia,
	"downloader": GroupMedia,
	"sticker":    GroupMedia,
	"notes":      GroupProductivity,
	"filters":    GroupProductivity,
	"scheduler":  GroupProductivity,
	"afk":        GroupProductivity,
	"broadcast":  GroupProductivity,
	"fun":        GroupFun,
	"ping":       GroupSystem,
	"alive":      GroupSystem,
	"system":     GroupSystem,
	"voice":      GroupVoice,
	"addon":      GroupAddon,
}

// GetGroup returns the logical group for a plugin name, defaulting to GroupCore if unmapped.
func GetGroup(pluginName string) Group {
	clean := strings.ToLower(strings.TrimSpace(pluginName))
	if g, ok := DefaultPluginGroups[clean]; ok {
		return g
	}
	return GroupCore
}

// GroupedPlugins organizes a slice of plugins into a map keyed by their logical Group.
func GroupedPlugins(plugins []Plugin) map[Group][]Plugin {
	res := make(map[Group][]Plugin)
	for _, p := range plugins {
		g := GetGroup(p.Name())
		res[g] = append(res[g], p)
	}
	return res
}
