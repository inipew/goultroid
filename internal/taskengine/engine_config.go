package taskengine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/tasks"
)

// PoolEngineConfig sets concurrency and queue parameters for a pool in TaskEngine.
type PoolEngineConfig struct {
	Concurrency int
	// MinConcurrency enables adaptive sizing when positive and lower than
	// Concurrency. Zero preserves the historical fixed-size behavior unless
	// ZeroIdle is explicitly enabled.
	MinConcurrency int
	// ZeroIdle permits a pool to retire every physical worker while empty.
	// It is explicit so existing configs that relied on MinConcurrency=0 as the
	// legacy fixed-size spelling retain their previous semantics.
	ZeroIdle bool
	// IdleTimeout retires adaptive workers above MinConcurrency. Zero uses the
	// default timeout.
	IdleTimeout   time.Duration
	BacklogLimit  int
	PayloadBudget int64
}

// Config configures the central execution coordinator (ADR 0006 §3.1).
type Config struct {
	Pools               map[tasks.PoolID]PoolEngineConfig
	ResultCapacity      int
	MaxTerminalRetained int
	DecisionTimeout     time.Duration
	// InboxCapacity bounds the single-writer control inbox. Zero means default.
	InboxCapacity int
	// MaxRetainedBytes caps admitted-but-unevicted memory (payload + bounded
	// output + failure text + per-record overhead). Zero means default.
	MaxRetainedBytes int64
	// MaxOutputBytes caps a single stored TaskResult.Output. Zero = default.
	MaxOutputBytes int64
	// MaxFailureBytes caps one stored failure message/detail. Zero = default.
	MaxFailureBytes int
	// DeliveryConcurrency is the fixed completion-callback worker count.
	// Zero means default.
	DeliveryConcurrency int
	// DeliveryQueueCap bounds both callback reservations and pending callback
	// delivery. Zero means max(ResultCapacity, 256).
	DeliveryQueueCap int
	// TerminalTTL evicts terminal records older than the TTL on the sweep
	// path. Zero disables TTL eviction (count/byte eviction still applies).
	TerminalTTL time.Duration
	// MaxScopeTombstones bounds revoked scope generations retained to fence
	// late submissions. Zero means the default.
	MaxScopeTombstones int
	// ResourceCapacities defines dispatch-time resource budgets by stable name.
	ResourceCapacities map[string]int64
}

func newDefaultConfig() Config {
	return Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general":       {Concurrency: 8, MinConcurrency: 0, ZeroIdle: true, IdleTimeout: 10 * time.Second, BacklogLimit: 200, PayloadBudget: 100 * 1024 * 1024},
			"interactive":   {Concurrency: 32, MinConcurrency: 0, ZeroIdle: true, IdleTimeout: 10 * time.Second, BacklogLimit: 128, PayloadBudget: 50 * 1024 * 1024},
			"download":      {Concurrency: 3, MinConcurrency: 0, ZeroIdle: true, IdleTimeout: 15 * time.Second, BacklogLimit: 50, PayloadBudget: 200 * 1024 * 1024},
			"media-process": {Concurrency: 2, MinConcurrency: 0, ZeroIdle: true, IdleTimeout: 20 * time.Second, BacklogLimit: 20, PayloadBudget: 200 * 1024 * 1024},
			"scheduler":     {Concurrency: 4, MinConcurrency: 0, ZeroIdle: true, IdleTimeout: 30 * time.Second, BacklogLimit: 100, PayloadBudget: 50 * 1024 * 1024},
		},
		ResultCapacity:      1000,
		MaxTerminalRetained: 1000,
		DecisionTimeout:     5 * time.Second,
		InboxCapacity:       2048,
		MaxRetainedBytes:    DefaultMaxRetainedBytes,
		MaxOutputBytes:      DefaultMaxOutputBytes,
		MaxFailureBytes:     DefaultMaxFailureBytes,
		DeliveryConcurrency: DefaultDeliveryConcurrency,
		TerminalTTL:         5 * time.Minute,
		MaxScopeTombstones:  4096,
	}
}

// NewDefaultConfig returns a fresh default configuration. Mutable maps are not
// shared across calls, so runtime construction never depends on package-global
// mutations.
func NewDefaultConfig() Config {
	return newDefaultConfig()
}

// DefaultConfig is retained for source compatibility. Runtime code must use
// NewDefaultConfig/newDefaultConfig instead of reading this mutable value.
var DefaultConfig = NewDefaultConfig()

const defaultPoolConcurrency = 4

func effectivePoolConcurrency(cfg PoolEngineConfig) int {
	if cfg.Concurrency > 0 {
		return cfg.Concurrency
	}
	return defaultPoolConcurrency
}

func effectivePoolMinimum(cfg PoolEngineConfig, concurrency int) int {
	if cfg.ZeroIdle {
		return 0
	}
	if cfg.MinConcurrency > 0 {
		return cfg.MinConcurrency
	}
	return concurrency
}

// ValidateConfig checks that pool and engine limits are non-negative.
func ValidateConfig(cfg Config) error {
	if cfg.ResultCapacity < 0 {
		return errors.New("taskengine: ResultCapacity cannot be negative")
	}
	if cfg.MaxTerminalRetained < 0 {
		return errors.New("taskengine: MaxTerminalRetained cannot be negative")
	}
	if cfg.DecisionTimeout < 0 {
		return errors.New("taskengine: DecisionTimeout cannot be negative")
	}
	if cfg.InboxCapacity < 0 {
		return errors.New("taskengine: InboxCapacity cannot be negative")
	}
	if cfg.MaxRetainedBytes < 0 {
		return errors.New("taskengine: MaxRetainedBytes cannot be negative")
	}
	if cfg.MaxOutputBytes < 0 {
		return errors.New("taskengine: MaxOutputBytes cannot be negative")
	}
	if cfg.MaxFailureBytes < 0 {
		return errors.New("taskengine: MaxFailureBytes cannot be negative")
	}
	if cfg.DeliveryConcurrency < 0 {
		return errors.New("taskengine: DeliveryConcurrency cannot be negative")
	}
	if cfg.DeliveryQueueCap < 0 {
		return errors.New("taskengine: DeliveryQueueCap cannot be negative")
	}
	if cfg.TerminalTTL < 0 {
		return errors.New("taskengine: TerminalTTL cannot be negative")
	}
	if cfg.MaxScopeTombstones < 0 {
		return errors.New("taskengine: MaxScopeTombstones cannot be negative")
	}
	for poolID, pcfg := range cfg.Pools {
		if poolID == "" {
			return errors.New("taskengine: pool ID cannot be empty")
		}
		if pcfg.Concurrency < 0 {
			return fmt.Errorf("taskengine: pool %s concurrency cannot be negative", poolID)
		}
		effectiveConcurrency := effectivePoolConcurrency(pcfg)
		if pcfg.MinConcurrency < 0 || pcfg.MinConcurrency > effectiveConcurrency {
			return fmt.Errorf("taskengine: pool %s minimum concurrency is invalid", poolID)
		}
		if pcfg.IdleTimeout < 0 {
			return fmt.Errorf("taskengine: pool %s idle timeout cannot be negative", poolID)
		}
		if pcfg.BacklogLimit < 0 {
			return fmt.Errorf("taskengine: pool %s backlog limit cannot be negative", poolID)
		}
		if pcfg.PayloadBudget < 0 {
			return fmt.Errorf("taskengine: pool %s payload budget cannot be negative", poolID)
		}
	}
	for name, capacity := range cfg.ResourceCapacities {
		if name == "" || capacity <= 0 {
			return errors.New("taskengine: resource capacities need a name and positive capacity")
		}
	}
	return nil
}

// NewEngine constructs a TaskEngine with the specified configuration.
func NewEngine(cfg Config) *Engine {
	configErr := ValidateConfig(cfg)
	defaults := newDefaultConfig()
	if cfg.ResultCapacity <= 0 {
		cfg.ResultCapacity = defaults.ResultCapacity
	}
	poolsSource := cfg.Pools
	if len(poolsSource) == 0 {
		poolsSource = defaults.Pools
	}
	copiedPools := make(map[tasks.PoolID]PoolEngineConfig, len(poolsSource))
	for k, v := range poolsSource {
		copiedPools[k] = v
	}
	cfg.Pools = copiedPools
	resourceCapacities := make(map[string]int64, len(cfg.ResourceCapacities))
	for name, capacity := range cfg.ResourceCapacities {
		resourceCapacities[name] = capacity
	}
	cfg.ResourceCapacities = resourceCapacities

	admPoolConfigs := make(map[tasks.PoolID]admission.PoolConfig, len(cfg.Pools))
	idleSlots := make(map[tasks.PoolID][]int, len(cfg.Pools))
	concurrencies := make(map[tasks.PoolID]int, len(cfg.Pools))
	minimums := make(map[tasks.PoolID]int, len(cfg.Pools))
	idleTimeouts := make(map[tasks.PoolID]time.Duration, len(cfg.Pools))
	workerRunning := make(map[tasks.PoolID][]bool, len(cfg.Pools))
	workerIdleSince := make(map[tasks.PoolID][]time.Time, len(cfg.Pools))
	workerCancels := make(map[tasks.PoolID][]context.CancelFunc, len(cfg.Pools))

	maxTerminal := cfg.MaxTerminalRetained
	if maxTerminal == 0 {
		maxTerminal = 1000
	}
	decisionTimeout := cfg.DecisionTimeout
	if decisionTimeout <= 0 {
		decisionTimeout = 5 * time.Second
	}
	inboxCap := cfg.InboxCapacity
	if inboxCap <= 0 {
		inboxCap = defaults.InboxCapacity
		if inboxCap <= 0 {
			inboxCap = 2048
		}
	}
	maxRetained := cfg.MaxRetainedBytes
	if maxRetained <= 0 {
		maxRetained = DefaultMaxRetainedBytes
	}
	maxOutput := cfg.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = DefaultMaxOutputBytes
	}
	maxFailure := cfg.MaxFailureBytes
	if maxFailure <= 0 {
		maxFailure = DefaultMaxFailureBytes
	}
	maxScopeTombstones := cfg.MaxScopeTombstones
	if maxScopeTombstones <= 0 {
		maxScopeTombstones = defaults.MaxScopeTombstones
	}
	deliveryWorkers := cfg.DeliveryConcurrency
	if deliveryWorkers <= 0 {
		deliveryWorkers = DefaultDeliveryConcurrency
	}
	deliveryCap := cfg.DeliveryQueueCap
	if deliveryCap <= 0 {
		deliveryCap = cfg.ResultCapacity
		if deliveryCap < 256 {
			deliveryCap = 256
		}
	}

	mailboxes := make(map[tasks.PoolID][]chan workerAssignment, len(cfg.Pools))
	for poolID, pcfg := range cfg.Pools {
		pcfg.Concurrency = effectivePoolConcurrency(pcfg)
		cfg.Pools[poolID] = pcfg
		concurrencies[poolID] = pcfg.Concurrency
		minimum := effectivePoolMinimum(pcfg, pcfg.Concurrency)
		minimums[poolID] = minimum
		idleTimeout := pcfg.IdleTimeout
		if idleTimeout <= 0 {
			idleTimeout = 10 * time.Second
		}
		idleTimeouts[poolID] = idleTimeout
		slots := make([]int, pcfg.Concurrency)
		// Mailboxes are allocated by spawnWorker per physical generation. Keeping
		// this slice nil-initialized avoids allocating channels for every maximum
		// slot when the default zero-idle pools have never executed work.
		mboxes := make([]chan workerAssignment, pcfg.Concurrency)
		for i := 0; i < pcfg.Concurrency; i++ {
			slots[i] = i
		}
		idleSlots[poolID] = slots[:0]
		mailboxes[poolID] = mboxes
		workerRunning[poolID] = make([]bool, pcfg.Concurrency)
		workerIdleSince[poolID] = make([]time.Time, pcfg.Concurrency)
		workerCancels[poolID] = make([]context.CancelFunc, pcfg.Concurrency)
		admPoolConfigs[poolID] = admission.PoolConfig{BacklogLimit: pcfg.BacklogLimit, PayloadBudget: pcfg.PayloadBudget}
	}

	return &Engine{
		config:              cfg,
		adm:                 admission.NewController(admPoolConfigs),
		idleSlots:           idleSlots,
		poolConcurrencies:   concurrencies,
		poolMinWorkers:      minimums,
		poolIdleTimeouts:    idleTimeouts,
		workerRunning:       workerRunning,
		workerIdleSince:     workerIdleSince,
		workerCancels:       workerCancels,
		resourceCapacity:    resourceCapacities,
		resourceUsed:        make(map[string]int64, len(resourceCapacities)),
		workerMailboxes:     mailboxes,
		resultCapacity:      cfg.ResultCapacity,
		registry:            make(map[tasks.TaskID]*taskRecord),
		cancelledScopes:     make(map[tasks.ScopeIdentity]tasks.Cause),
		maxScopeTombstones:  maxScopeTombstones,
		maxTerminalRetained: maxTerminal,
		maxRetainedBytes:    maxRetained,
		maxOutputBytes:      maxOutput,
		maxFailureBytes:     maxFailure,
		terminalTTL:         cfg.TerminalTTL,
		decisionTimeout:     decisionTimeout,
		inboxCap:            inboxCap,
		delivery:            newCompletionDelivery(deliveryWorkers, deliveryCap),
		durability:          newDurabilityLane(defaultDurabilityConcurrency, cfg.ResultCapacity),
		drainDone:           make(chan struct{}),
		runtimeDone:         make(chan struct{}),
		configErr:           configErr,
	}
}
