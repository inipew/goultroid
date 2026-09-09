package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/platform/audit"
	"github.com/inipew/goultroid/internal/resource"
)

var (
	ErrBinaryNotAllowed = errors.New("executable binary not in allowlist")
	ErrOutputTooLarge   = errors.New("process output exceeded maximum buffer limit")
)

// Manager coordinates execution of external binaries, enforcing an allowlist,
// output bounds, and resource tracking.
type Manager struct {
	mu           sync.RWMutex
	allowlist    map[string]bool
	maxOutputLen int
	resourceMgr  *resource.Manager
	auditor      audit.Auditor
	procCounter  atomic.Uint64
}

// Executor is a process manager scoped to a single immutable owner.
// Plugin code receives this facade so resource and audit attribution cannot be spoofed.
type Executor struct {
	manager *Manager
	owner   string
}

// ForOwner returns a process executor permanently bound to owner.
func (m *Manager) ForOwner(owner string) *Executor {
	return &Executor{manager: m, owner: strings.TrimSpace(owner)}
}

// Execute runs an approved binary and attributes it to the bound owner.
func (e *Executor) Execute(ctx context.Context, binary string, args ...string) ([]byte, []byte, error) {
	if e == nil || e.manager == nil {
		return nil, nil, errors.New("process manager not configured")
	}
	return e.manager.Execute(ctx, e.owner, binary, args...)
}

// NewManager creates a ProcessManager with allowed binary names and max output limit.
func NewManager(allowedBinaries []string, maxOutputLen int, rm *resource.Manager) *Manager {
	if maxOutputLen <= 0 {
		maxOutputLen = 10 * 1024 * 1024 // 10MB default
	}

	allowMap := make(map[string]bool)
	for _, bin := range allowedBinaries {
		allowMap[strings.TrimSpace(bin)] = true
	}

	return &Manager{
		allowlist:    allowMap,
		maxOutputLen: maxOutputLen,
		resourceMgr:  rm,
	}
}

// SetAuditor attaches an audit logger to record process executions.
func (m *Manager) SetAuditor(a audit.Auditor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auditor = a
}

// Allow adds a binary name to the allowlist.
func (m *Manager) Allow(binary string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allowlist[strings.TrimSpace(binary)] = true
}

// IsAllowed checks if a binary is permitted to execute.
func (m *Manager) IsAllowed(binary string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.allowlist["*"] {
		return true
	}
	return m.allowlist[strings.TrimSpace(binary)]
}

// Execute runs an approved binary with tracking in ResourceManager and bounded output.
func (m *Manager) Execute(ctx context.Context, owner, binary string, args ...string) ([]byte, []byte, error) {
	if !m.IsAllowed(binary) {
		m.mu.RLock()
		auditor := m.auditor
		m.mu.RUnlock()
		if auditor != nil {
			_ = auditor.Record(ctx, audit.AuditEvent{
				Action: "process.execute.denied",
				Target: binary,
				Details: map[string]any{
					"owner": owner,
					"args":  args,
				},
			})
		}
		return nil, nil, fmt.Errorf("%w: %s", ErrBinaryNotAllowed, binary)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.RLock()
	auditor := m.auditor
	m.mu.RUnlock()
	if auditor != nil {
		_ = auditor.Record(ctx, audit.AuditEvent{
			Action: "process.execute",
			Target: binary,
			Details: map[string]any{
				"owner": owner,
				"args":  args,
			},
		})
	}

	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	procID := fmt.Sprintf("process:%s:%s:%d", owner, binary, m.procCounter.Add(1))
	if m.resourceMgr != nil {
		if err := m.resourceMgr.Register(resource.Resource{
			ID:        procID,
			Owner:     owner,
			Type:      resource.TypeProcess,
			CreatedAt: time.Now().UTC(),
			Metadata: map[string]string{
				"binary": binary,
			},
		}); err != nil {
			return nil, nil, fmt.Errorf("track process resource: %w", err)
		}
		defer func() {
			_ = m.resourceMgr.Release(procID)
		}()
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("failed to start process %s: %w", binary, err)
	}

	err := cmd.Wait()

	if stdout.Len() > m.maxOutputLen || stderr.Len() > m.maxOutputLen {
		return stdout.Bytes(), stderr.Bytes(), ErrOutputTooLarge
	}

	if err != nil {
		return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("process %s exited with error: %w", binary, err)
	}

	return stdout.Bytes(), stderr.Bytes(), nil
}

// StartCmd starts a prepared exec.Cmd and registers it in the ResourceManager.
// It returns the generated process ID that must be released upon termination.
func (m *Manager) StartCmd(ctx context.Context, owner string, cmd *exec.Cmd) (string, error) {
	if cmd == nil {
		return "", errors.New("exec.Cmd cannot be nil")
	}
	binary := filepath.Base(cmd.Path)
	procID := fmt.Sprintf("process:%s:%s:%d", owner, binary, m.procCounter.Add(1))
	if m.resourceMgr != nil {
		if err := m.resourceMgr.Register(resource.Resource{
			ID:        procID,
			Owner:     owner,
			Type:      resource.TypeProcess,
			CreatedAt: time.Now().UTC(),
			Metadata: map[string]string{
				"binary": binary,
			},
		}); err != nil {
			return "", fmt.Errorf("track process resource: %w", err)
		}
	}

	if err := cmd.Start(); err != nil {
		if m.resourceMgr != nil {
			_ = m.resourceMgr.Release(procID)
		}
		return "", err
	}
	return procID, nil
}

// ReleaseCmd releases a process registration from the ResourceManager.
func (m *Manager) ReleaseCmd(procID string) {
	if m.resourceMgr != nil && procID != "" {
		_ = m.resourceMgr.Release(procID)
	}
}
