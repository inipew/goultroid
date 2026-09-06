package addon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"go.uber.org/zap"
)

// Manager coordinates addon installation, validation, capability gating,
// external-process lifecycle, and database persistence.
type Manager struct {
	db         *database.DB
	gate       *CapabilityGate
	broker     *CapabilityBroker
	appVersion string
	logger     *zap.Logger

	runtimeMu sync.RWMutex
	runtimes  map[string]*ExternalRuntime
}

func NewManager(db *database.DB, gate *CapabilityGate, appVersion string, logger *zap.Logger) *Manager {
	if logger == nil { logger = zap.NewNop() }
	if gate == nil { gate = NewCapabilityGate() }
	if appVersion == "" { appVersion = "1.0.0" }
	return &Manager{
		db: db, gate: gate, broker: NewCapabilityBroker(gate), appVersion: appVersion,
		logger: logger.Named("addon"), runtimes: make(map[string]*ExternalRuntime),
	}
}

func (m *Manager) Gate() *CapabilityGate { return m.gate }
func (m *Manager) Broker() *CapabilityBroker { return m.broker }

func (m *Manager) LoadInstalled(ctx context.Context) error {
	if m.db == nil { return nil }
	addons, err := m.db.ListAddons(ctx)
	if err != nil { return fmt.Errorf("failed to list installed addons: %w", err) }
	count := 0
	for _, a := range addons {
		if a.Status == string(StatusActive) {
			m.gate.Register(a.Name, parseCapabilitiesString(a.Capabilities))
			count++
		}
	}
	m.logger.Info("loaded active external addons", zap.Int("active_count", count))
	return nil
}

func (m *Manager) Install(ctx context.Context, rawManifest []byte, sourceURL string) (*Manifest, error) {
	manifest, err := ParseManifest(rawManifest)
	if err != nil { return nil, err }
	if err := CheckCompatibility(manifest, m.appVersion); err != nil { return nil, err }
	if m.db != nil {
		existing, err := m.db.GetAddon(ctx, manifest.Name)
		if err == nil && existing != nil { return nil, fmt.Errorf("%w: addon %q is already installed", ErrAddonAlreadyInstalled, manifest.Name) }
		now := time.Now().UTC()
		rec := &database.AddonRecord{
			Name: manifest.Name, Version: manifest.Version, Description: manifest.Description,
			Author: manifest.Author, SourceURL: sourceURL, Status: string(StatusActive),
			Capabilities: joinCapabilities(manifest.Capabilities), MinVersion: manifest.MinGoUltroid,
			InstalledAt: now, UpdatedAt: now,
		}
		if err := m.db.SaveAddon(ctx, rec); err != nil { return nil, fmt.Errorf("failed to persist addon: %w", err) }
	}
	m.gate.Register(manifest.Name, manifest.Capabilities)
	m.logger.Info("installed addon", zap.String("name", manifest.Name), zap.String("version", manifest.Version), zap.Int("capabilities", len(manifest.Capabilities)))
	return manifest, nil
}

// StartRuntime validates and starts an installed addon executable. The optional
// SHA-256 digest is checked before process creation when supplied.
func (m *Manager) StartRuntime(ctx context.Context, name, executable, expectedSHA256 string) error {
	cleanName := strings.ToLower(strings.TrimSpace(name))
	if cleanName == "" { return ErrAddonNotFound }
	manifest, err := m.manifestForRuntime(ctx, cleanName)
	if err != nil { return err }
	if expectedSHA256 != "" {
		if err := VerifySHA256(executable, expectedSHA256); err != nil { return err }
	}

	runtime := NewExternalRuntime(*manifest, executable, m.broker)
	if err := runtime.Start(ctx); err != nil { return err }

	m.runtimeMu.Lock()
	old := m.runtimes[cleanName]
	m.runtimes[cleanName] = runtime
	m.runtimeMu.Unlock()
	if old != nil { _ = old.Stop() }
	m.logger.Info("started addon runtime", zap.String("name", cleanName), zap.String("executable", executable))
	return nil
}

func (m *Manager) StopRuntime(name string) error {
	cleanName := strings.ToLower(strings.TrimSpace(name))
	m.runtimeMu.Lock()
	runtime := m.runtimes[cleanName]
	delete(m.runtimes, cleanName)
	m.runtimeMu.Unlock()
	if runtime == nil { return nil }
	if err := runtime.Stop(); err != nil { return fmt.Errorf("stop addon %q: %w", cleanName, err) }
	return nil
}

func (m *Manager) RuntimeRunning(name string) bool {
	cleanName := strings.ToLower(strings.TrimSpace(name))
	m.runtimeMu.RLock()
	runtime := m.runtimes[cleanName]
	m.runtimeMu.RUnlock()
	return runtime != nil && runtime.Running()
}

// CallRuntime performs an IPC request. Privileged calls must use
// CallRuntimeWithCapability so the manifest gate is checked at the boundary.
func (m *Manager) CallRuntime(ctx context.Context, name, method string, params any) (interface{}, error) {
	cleanName := strings.ToLower(strings.TrimSpace(name))
	m.runtimeMu.RLock()
	runtime := m.runtimes[cleanName]
	m.runtimeMu.RUnlock()
	if runtime == nil { return nil, ErrAddonDisabled }
	result, err := runtime.Call(ctx, method, params)
	if err != nil { return nil, err }
	return result, nil
}

func (m *Manager) CallRuntimeWithCapability(ctx context.Context, name string, capability Capability, method string, params any) (interface{}, error) {
	cleanName := strings.ToLower(strings.TrimSpace(name))
	if err := m.broker.Authorize(cleanName, capability); err != nil { return nil, err }
	return m.CallRuntime(ctx, cleanName, method, params)
}

func (m *Manager) ShutdownRuntimes() error {
	m.runtimeMu.Lock()
	runtimes := make(map[string]*ExternalRuntime, len(m.runtimes))
	for name, runtime := range m.runtimes { runtimes[name] = runtime }
	m.runtimes = make(map[string]*ExternalRuntime)
	m.runtimeMu.Unlock()
	var firstErr error
	for name, runtime := range runtimes {
		if err := runtime.Stop(); err != nil && firstErr == nil { firstErr = fmt.Errorf("stop addon %q: %w", name, err) }
	}
	return firstErr
}

func (m *Manager) manifestForRuntime(ctx context.Context, name string) (*Manifest, error) {
	if m.db == nil { return nil, ErrAddonNotFound }
	rec, err := m.db.GetAddon(ctx, name)
	if err != nil || rec == nil { return nil, ErrAddonNotFound }
	if rec.Status != string(StatusActive) { return nil, ErrAddonDisabled }
	return &Manifest{Name: rec.Name, Version: rec.Version, Description: rec.Description, Author: rec.Author, MinGoUltroid: rec.MinVersion, Capabilities: parseCapabilitiesString(rec.Capabilities)}, nil
}

func (m *Manager) Uninstall(ctx context.Context, name string) error {
	cleanName := strings.ToLower(strings.TrimSpace(name))
	if err := m.StopRuntime(cleanName); err != nil { return err }
	if m.db != nil {
		existing, err := m.db.GetAddon(ctx, cleanName)
		if err != nil || existing == nil { return ErrAddonNotFound }
		if err := m.db.DeleteAddon(ctx, cleanName); err != nil { return fmt.Errorf("failed to delete addon from db: %w", err) }
	}
	m.gate.Unregister(cleanName)
	m.logger.Info("uninstalled addon", zap.String("name", cleanName))
	return nil
}

func (m *Manager) Enable(ctx context.Context, name string) error {
	cleanName := strings.ToLower(strings.TrimSpace(name))
	if m.db == nil { return nil }
	rec, err := m.db.GetAddon(ctx, cleanName)
	if err != nil || rec == nil { return ErrAddonNotFound }
	if rec.Status == string(StatusActive) { return nil }
	if err := m.db.SetAddonStatus(ctx, cleanName, string(StatusActive)); err != nil { return err }
	m.gate.Register(cleanName, parseCapabilitiesString(rec.Capabilities))
	m.logger.Info("enabled addon", zap.String("name", cleanName))
	return nil
}

func (m *Manager) Disable(ctx context.Context, name string) error {
	cleanName := strings.ToLower(strings.TrimSpace(name))
	if err := m.StopRuntime(cleanName); err != nil { return err }
	if m.db == nil { return nil }
	rec, err := m.db.GetAddon(ctx, cleanName)
	if err != nil || rec == nil { return ErrAddonNotFound }
	if rec.Status == string(StatusDisabled) { return nil }
	if err := m.db.SetAddonStatus(ctx, cleanName, string(StatusDisabled)); err != nil { return err }
	m.gate.Unregister(cleanName)
	m.logger.Info("disabled addon", zap.String("name", cleanName))
	return nil
}

func (m *Manager) List(ctx context.Context) ([]*database.AddonRecord, error) {
	if m.db == nil { return nil, nil }
	return m.db.ListAddons(ctx)
}

func (m *Manager) Get(ctx context.Context, name string) (*database.AddonRecord, error) {
	if m.db == nil { return nil, ErrAddonNotFound }
	rec, err := m.db.GetAddon(ctx, name)
	if err != nil || rec == nil { return nil, ErrAddonNotFound }
	return rec, nil
}

func parseCapabilitiesString(s string) []Capability {
	if s == "" { return nil }
	parts := strings.Split(s, ",")
	res := make([]Capability, 0, len(parts))
	for _, p := range parts {
		c := Capability(strings.TrimSpace(p))
		if ValidCapabilities[c] { res = append(res, c) }
	}
	return res
}

func joinCapabilities(caps []Capability) string {
	strs := make([]string, len(caps))
	for i, c := range caps { strs[i] = string(c) }
	return strings.Join(strs, ",")
}
