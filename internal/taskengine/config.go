package taskengine

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

// PoolLimits bound logical waiting work before a physical permit is assigned.
type PoolLimits struct {
	Workers         int
	MaxWaiting      int
	MaxWaitingBytes int64
}

// OwnerLimits are global quota-owner bounds across all pools.
type OwnerLimits struct {
	MaxWaiting      int
	MaxActive       int
	MaxWaitingBytes int64
	Weight          int
}

// Config is validated before TaskEngine Start. Zero values are not interpreted
// as hidden fallbacks; construction must provide explicit bounded capacities.
type Config struct {
	Pools                  map[tasks.PoolID]PoolLimits
	ClassQuantum           map[tasks.PriorityClass]int
	ResourceCapacity       map[string]uint32
	ResultCredits          int
	ControlInboxCapacity   int
	PrepareQueueCapacity   int
	PersistenceCapacity    int
	DefaultOwner           OwnerLimits
	Owners                 map[tasks.QuotaOwner]OwnerLimits
	AdmissionDecisionLimit time.Duration
	QueueTimeout           time.Duration
	PrepareTimeout         time.Duration
	ExecutionTimeout       time.Duration
	ResultRetention        time.Duration
}

func (c Config) Validate() error {
	if len(c.Pools) == 0 {
		return errors.New("at least one worker pool is required")
	}
	for pool, limits := range c.Pools {
		if strings.TrimSpace(string(pool)) == "" {
			return errors.New("worker pool ID cannot be empty")
		}
		if limits.Workers <= 0 || limits.MaxWaiting <= 0 || limits.MaxWaitingBytes <= 0 {
			return fmt.Errorf("worker pool %q requires positive workers and waiting limits", pool)
		}
	}
	for class := tasks.PriorityInteractive; class <= tasks.PriorityMaintenance; class++ {
		if c.ClassQuantum[class] <= 0 {
			return fmt.Errorf("priority class %d requires a positive quantum", class)
		}
	}
	for name, units := range c.ResourceCapacity {
		if strings.TrimSpace(name) == "" || units == 0 {
			return errors.New("resource capacity requires a non-empty name and positive units")
		}
	}
	if c.ResultCredits <= 0 || c.ControlInboxCapacity <= 0 || c.PrepareQueueCapacity <= 0 || c.PersistenceCapacity <= 0 {
		return errors.New("execution capacities must be positive")
	}
	if c.DefaultOwner.MaxWaiting <= 0 || c.DefaultOwner.MaxActive <= 0 || c.DefaultOwner.MaxWaitingBytes <= 0 || c.DefaultOwner.Weight <= 0 {
		return errors.New("default owner limits must be positive")
	}
	for owner, limits := range c.Owners {
		if strings.TrimSpace(string(owner)) == "" {
			return errors.New("owner override requires a non-empty owner")
		}
		if limits.MaxWaiting <= 0 || limits.MaxActive <= 0 || limits.MaxWaitingBytes <= 0 || limits.Weight <= 0 {
			return fmt.Errorf("owner %q limits must be positive", owner)
		}
	}
	if c.AdmissionDecisionLimit <= 0 || c.QueueTimeout <= 0 || c.PrepareTimeout <= 0 || c.ExecutionTimeout <= 0 || c.ResultRetention <= 0 {
		return errors.New("execution deadlines and retention must be positive")
	}
	return nil
}
