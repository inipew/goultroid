package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	procCounter  atomic.Uint64
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
	return m.allowlist[strings.TrimSpace(binary)]
}

// Execute runs an approved binary with tracking in ResourceManager and bounded output.
func (m *Manager) Execute(ctx context.Context, owner, binary string, args ...string) ([]byte, []byte, error) {
	if !m.IsAllowed(binary) {
		return nil, nil, fmt.Errorf("%w: %s", ErrBinaryNotAllowed, binary)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	procID := fmt.Sprintf("process:%s:%s:%d", owner, binary, m.procCounter.Add(1))
	if m.resourceMgr != nil {
		_ = m.resourceMgr.Register(resource.Resource{
			ID:        procID,
			Owner:     owner,
			Type:      resource.TypeProcess,
			CreatedAt: time.Now().UTC(),
			Metadata: map[string]string{
				"binary": binary,
			},
		})
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
