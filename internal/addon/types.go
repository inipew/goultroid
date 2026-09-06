package addon

import (
	"errors"
)

// Capability defines a granular permission granted to an external addon.
type Capability string

const (
	CapTelegramRead    Capability = "telegram.read"
	CapTelegramSend    Capability = "telegram.send"
	CapTelegramDelete  Capability = "telegram.delete"
	CapMediaDownload   Capability = "media.download"
	CapProcessExecute  Capability = "process.execute"
	CapStorageWrite    Capability = "storage.write"
	CapSchedulerCreate Capability = "scheduler.create"
	CapSchedulerCancel Capability = "scheduler.cancel"
)

// ValidCapabilities is the set of all authorized capability identifiers.
var ValidCapabilities = map[Capability]bool{
	CapTelegramRead:    true,
	CapTelegramSend:    true,
	CapTelegramDelete:  true,
	CapMediaDownload:   true,
	CapProcessExecute:  true,
	CapStorageWrite:    true,
	CapSchedulerCreate: true,
	CapSchedulerCancel: true,
}

// Status represents the operational status of an installed addon.
type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

// Manifest defines the declaration schema of an external addon.
type Manifest struct {
	Name         string       `yaml:"name" json:"name"`
	Version      string       `yaml:"version" json:"version"`
	MinGoUltroid string       `yaml:"min_goultroid" json:"min_goultroid"`
	Description  string       `yaml:"description" json:"description"`
	Author       string       `yaml:"author" json:"author"`
	Commands     []string     `yaml:"commands" json:"commands"`
	Capabilities []Capability `yaml:"capabilities" json:"capabilities"`
}

var (
	ErrInvalidManifest        = errors.New("invalid addon manifest")
	ErrIncompatibleVersion    = errors.New("addon incompatible with current GoUltroid version")
	ErrUnauthorizedCapability = errors.New("addon capability permission denied")
	ErrAddonNotFound          = errors.New("addon not found")
	ErrAddonAlreadyInstalled  = errors.New("addon is already installed")
	ErrAddonDisabled          = errors.New("addon is currently disabled")
)
