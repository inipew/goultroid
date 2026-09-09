package filesystem

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/resource"
)

// Manager provides scoped filesystem access for plugins, enforcing path confinement
// and managing temporary file lifecycles.
type Manager struct {
	baseDataDir  string
	baseCacheDir string
	baseTempDir  string
	resourceMgr  *resource.Manager
}

// Scope is a filesystem manager bound to a single immutable plugin owner.
type Scope struct {
	manager *Manager
	owner   string
}

// ForOwner returns filesystem access permanently scoped to owner.
func (m *Manager) ForOwner(owner string) *Scope {
	return &Scope{manager: m, owner: strings.TrimSpace(owner)}
}

func (s *Scope) DataDir() (string, error) { return s.manager.PluginDataDir(s.owner) }
func (s *Scope) TempDir() (string, error) { return s.manager.PluginTempDir(s.owner) }
func (s *Scope) CreateTempFile(pattern string) (*os.File, error) {
	return s.manager.CreateTempFile(s.owner, pattern)
}
func (s *Scope) RemoveTempFile(path string) error {
	if err := s.validateTempPath(path); err != nil {
		return err
	}
	return s.manager.RemoveTempFile(path)
}
func (s *Scope) CreateTempDir(pattern string) (string, error) {
	return s.manager.CreateTempDir(s.owner, pattern)
}
func (s *Scope) RemoveTempDir(path string) error {
	if err := s.validateTempPath(path); err != nil {
		return err
	}
	return s.manager.RemoveTempDir(path)
}
func (s *Scope) SafePath(rootDir, relPath string) (string, error) {
	return s.manager.SafePath(rootDir, relPath)
}

func (s *Scope) validateTempPath(path string) error {
	root, err := s.manager.PluginTempDir(s.owner)
	if err != nil {
		return err
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if cleanPath == cleanRoot || !strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator)) {
		return fmt.Errorf("path %q is outside plugin %q temp scope", path, s.owner)
	}
	return nil
}

// NewManager creates a filesystem manager with root directories for data, cache, and temp files.
func NewManager(baseData, baseCache, baseTemp string, rm *resource.Manager) (*Manager, error) {
	if baseData == "" {
		baseData = "data"
	}
	if baseCache == "" {
		baseCache = filepath.Join(baseData, "cache")
	}
	if baseTemp == "" {
		baseTemp = filepath.Join(baseData, "tmp")
	}

	for _, dir := range []string{baseData, baseCache, baseTemp} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %q: %w", dir, err)
		}
	}

	return &Manager{
		baseDataDir:  baseData,
		baseCacheDir: baseCache,
		baseTempDir:  baseTemp,
		resourceMgr:  rm,
	}, nil
}

// PluginDataDir returns the scoped data directory for a plugin, creating it if needed.
func (m *Manager) PluginDataDir(pluginID string) (string, error) {
	if strings.TrimSpace(pluginID) == "" {
		return "", errors.New("plugin ID cannot be empty")
	}
	dir := filepath.Join(m.baseDataDir, "plugins", pluginID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create plugin data dir: %w", err)
	}
	return dir, nil
}

// PluginTempDir returns the scoped temp directory for a plugin, creating it if needed.
func (m *Manager) PluginTempDir(pluginID string) (string, error) {
	if strings.TrimSpace(pluginID) == "" {
		return "", errors.New("plugin ID cannot be empty")
	}
	dir := filepath.Join(m.baseTempDir, "plugins", pluginID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create plugin temp dir: %w", err)
	}
	return dir, nil
}

// CreateTempFile creates a scoped temporary file and tracks it in the ResourceManager.
func (m *Manager) CreateTempFile(pluginID, pattern string) (*os.File, error) {
	dir, err := m.PluginTempDir(pluginID)
	if err != nil {
		return nil, err
	}

	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}

	if m.resourceMgr != nil {
		_ = m.resourceMgr.Register(resource.Resource{
			ID:        f.Name(),
			Owner:     "plugin:" + pluginID,
			Type:      resource.TypeTempFile,
			CreatedAt: time.Now().UTC(),
		})
	}

	return f, nil
}

// RemoveTempFile removes a tracked temporary file and releases it from the ResourceManager.
func (m *Manager) RemoveTempFile(path string) error {
	err := os.Remove(path)
	if m.resourceMgr != nil {
		_ = m.resourceMgr.Release(path)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// CreateTempDir creates a scoped temporary directory and tracks it in the ResourceManager.
func (m *Manager) CreateTempDir(pluginID, pattern string) (string, error) {
	dir, err := m.PluginTempDir(pluginID)
	if err != nil {
		return "", err
	}

	tempDir, err := os.MkdirTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}

	if m.resourceMgr != nil {
		_ = m.resourceMgr.Register(resource.Resource{
			ID:        tempDir,
			Owner:     "plugin:" + pluginID,
			Type:      resource.TypeTempDir,
			CreatedAt: time.Now().UTC(),
		})
	}

	return tempDir, nil
}

// RemoveTempDir recursively removes a tracked temporary directory and releases it from the ResourceManager.
func (m *Manager) RemoveTempDir(path string) error {
	err := os.RemoveAll(path)
	if m.resourceMgr != nil {
		_ = m.resourceMgr.Release(path)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SafePath confines a requested relative path within the plugin's root directory, preventing directory traversal.
func (m *Manager) SafePath(rootDir, relPath string) (string, error) {
	cleanRoot := filepath.Clean(rootDir)
	joined := filepath.Join(cleanRoot, relPath)
	cleanTarget := filepath.Clean(joined)

	if !strings.HasPrefix(cleanTarget, cleanRoot+string(filepath.Separator)) && cleanTarget != cleanRoot {
		return "", fmt.Errorf("path %q escapes confined root directory %q", relPath, rootDir)
	}
	return cleanTarget, nil
}
