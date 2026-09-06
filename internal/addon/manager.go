package addon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"go.uber.org/zap"
)

// Manager coordinates addon installation, validation, capability gating, and database persistence.
type Manager struct {
	db         *database.DB
	gate       *CapabilityGate
	appVersion string
	logger     *zap.Logger
}

// NewManager creates a new Addon Manager.
func NewManager(db *database.DB, gate *CapabilityGate, appVersion string, logger *zap.Logger) *Manager {
	if logger == nil {
		logger = zap.NewNop()
	}
	if gate == nil {
		gate = NewCapabilityGate()
	}
	if appVersion == "" {
		appVersion = "1.0.0"
	}

	return &Manager{
		db:         db,
		gate:       gate,
		appVersion: appVersion,
		logger:     logger.Named("addon"),
	}
}

// Gate returns the underlying capability gate.
func (m *Manager) Gate() *CapabilityGate {
	return m.gate
}

// LoadInstalled restores capability registrations from the database on startup.
func (m *Manager) LoadInstalled(ctx context.Context) error {
	if m.db == nil {
		return nil
	}

	addons, err := m.db.ListAddons(ctx)
	if err != nil {
		return fmt.Errorf("failed to list installed addons: %w", err)
	}

	count := 0
	for _, a := range addons {
		if a.Status == string(StatusActive) {
			caps := parseCapabilitiesString(a.Capabilities)
			m.gate.Register(a.Name, caps)
			count++
		}
	}

	m.logger.Info("loaded active external addons", zap.Int("active_count", count))
	return nil
}

// Install parses manifest bytes, validates version compatibility and capabilities, and registers the addon.
func (m *Manager) Install(ctx context.Context, rawManifest []byte, sourceURL string) (*Manifest, error) {
	manifest, err := ParseManifest(rawManifest)
	if err != nil {
		return nil, err
	}

	if err := CheckCompatibility(manifest, m.appVersion); err != nil {
		return nil, err
	}

	if m.db != nil {
		existing, err := m.db.GetAddon(ctx, manifest.Name)
		if err == nil && existing != nil {
			return nil, fmt.Errorf("%w: addon %q is already installed", ErrAddonAlreadyInstalled, manifest.Name)
		}

		capStr := joinCapabilities(manifest.Capabilities)
		rec := &database.AddonRecord{
			Name:         manifest.Name,
			Version:      manifest.Version,
			Description:  manifest.Description,
			Author:       manifest.Author,
			SourceURL:    sourceURL,
			Status:       string(StatusActive),
			Capabilities: capStr,
			MinVersion:   manifest.MinGoUltroid,
			InstalledAt:  time.Now().UTC(),
			UpdatedAt:    time.Now().UTC(),
		}

		if err := m.db.SaveAddon(ctx, rec); err != nil {
			return nil, fmt.Errorf("failed to persist addon: %w", err)
		}
	}

	m.gate.Register(manifest.Name, manifest.Capabilities)
	m.logger.Info("installed addon",
		zap.String("name", manifest.Name),
		zap.String("version", manifest.Version),
		zap.Int("capabilities", len(manifest.Capabilities)),
	)

	return manifest, nil
}

// Uninstall deletes the addon from the registry and revokes all capability permissions.
func (m *Manager) Uninstall(ctx context.Context, name string) error {
	cleanName := strings.ToLower(strings.TrimSpace(name))

	if m.db != nil {
		existing, err := m.db.GetAddon(ctx, cleanName)
		if err != nil || existing == nil {
			return ErrAddonNotFound
		}

		if err := m.db.DeleteAddon(ctx, cleanName); err != nil {
			return fmt.Errorf("failed to delete addon from db: %w", err)
		}
	}

	m.gate.Unregister(cleanName)
	m.logger.Info("uninstalled addon", zap.String("name", cleanName))
	return nil
}

// Enable activates an installed addon and restores its capability permissions.
func (m *Manager) Enable(ctx context.Context, name string) error {
	cleanName := strings.ToLower(strings.TrimSpace(name))

	if m.db == nil {
		return nil
	}

	rec, err := m.db.GetAddon(ctx, cleanName)
	if err != nil || rec == nil {
		return ErrAddonNotFound
	}

	if rec.Status == string(StatusActive) {
		return nil
	}

	if err := m.db.SetAddonStatus(ctx, cleanName, string(StatusActive)); err != nil {
		return err
	}

	caps := parseCapabilitiesString(rec.Capabilities)
	m.gate.Register(cleanName, caps)
	m.logger.Info("enabled addon", zap.String("name", cleanName))
	return nil
}

// Disable deactivates an addon and revokes its capabilities without deleting it.
func (m *Manager) Disable(ctx context.Context, name string) error {
	cleanName := strings.ToLower(strings.TrimSpace(name))

	if m.db == nil {
		return nil
	}

	rec, err := m.db.GetAddon(ctx, cleanName)
	if err != nil || rec == nil {
		return ErrAddonNotFound
	}

	if rec.Status == string(StatusDisabled) {
		return nil
	}

	if err := m.db.SetAddonStatus(ctx, cleanName, string(StatusDisabled)); err != nil {
		return err
	}

	m.gate.Unregister(cleanName)
	m.logger.Info("disabled addon", zap.String("name", cleanName))
	return nil
}

// List returns all installed addons.
func (m *Manager) List(ctx context.Context) ([]*database.AddonRecord, error) {
	if m.db == nil {
		return nil, nil
	}
	return m.db.ListAddons(ctx)
}

// Get fetches details for a specific addon.
func (m *Manager) Get(ctx context.Context, name string) (*database.AddonRecord, error) {
	if m.db == nil {
		return nil, ErrAddonNotFound
	}
	rec, err := m.db.GetAddon(ctx, name)
	if err != nil || rec == nil {
		return nil, ErrAddonNotFound
	}
	return rec, nil
}

func parseCapabilitiesString(s string) []Capability {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	res := make([]Capability, 0, len(parts))
	for _, p := range parts {
		c := Capability(strings.TrimSpace(p))
		if ValidCapabilities[c] {
			res = append(res, c)
		}
	}
	return res
}

func joinCapabilities(caps []Capability) string {
	strs := make([]string, len(caps))
	for i, c := range caps {
		strs[i] = string(c)
	}
	return strings.Join(strs, ",")
}
