package config

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

// Config holds all configuration values for the application.
type Config struct {
	AppID                           int
	AppHash                         string
	Phone                           string
	SessionFile                     string
	DatabasePath                    string
	Prefix                          string
	OwnerID                         int64
	SudoUsers                       []int64
	LogLevel                        string
	BotToken                        string
	Mode                            string
	TaskEngine                      taskengine.Config
	TaskEngineDurabilityConcurrency int
}

// Load reads configuration from environment variables and validates all fields.
func Load() (*Config, error) {
	return LoadFrom(os.Getenv)
}

// LoadFrom reads configuration through an isolated lookup function. Callers
// loading a specific file can avoid mutating or inheriting process-global state.
func LoadFrom(lookup func(string) string) (*Config, error) {
	if lookup == nil {
		return nil, fmt.Errorf("configuration lookup is nil")
	}
	appIDStr := lookup("APP_ID")
	if appIDStr == "" {
		return nil, fmt.Errorf("APP_ID is required")
	}
	appID, err := strconv.Atoi(appIDStr)
	if err != nil || appID <= 0 {
		return nil, fmt.Errorf("invalid APP_ID: must be a positive integer")
	}

	appHash := strings.TrimSpace(lookup("APP_HASH"))
	if appHash == "" {
		return nil, fmt.Errorf("APP_HASH is required")
	}

	phone, err := NormalizePhone(lookup("PHONE"))
	if err != nil {
		return nil, err
	}

	sessionFile := strings.TrimSpace(lookup("SESSION_FILE"))
	if sessionFile == "" {
		sessionFile = "data/session.json"
	}

	databasePath := strings.TrimSpace(lookup("DATABASE_PATH"))
	if databasePath == "" {
		databasePath = "data/goultroid.db"
	}

	prefix := lookup("PREFIX")
	if prefix == "" {
		prefix = "."
	}

	var ownerID int64
	if ownerStr := strings.TrimSpace(lookup("OWNER_ID")); ownerStr != "" {
		val, err := strconv.ParseInt(ownerStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid OWNER_ID: %w", err)
		}
		ownerID = val
	}

	var sudoUsers []int64
	if sudoStr := strings.TrimSpace(lookup("SUDO_USERS")); sudoStr != "" {
		parts := strings.Split(sudoStr, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			uid, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid sudo user ID %q: %w", part, err)
			}
			sudoUsers = append(sudoUsers, uid)
		}
	}

	logLevel := strings.ToLower(strings.TrimSpace(lookup("LOG_LEVEL")))
	if logLevel == "" {
		logLevel = "info"
	}

	botToken := strings.TrimSpace(lookup("BOT_TOKEN"))
	mode := strings.ToLower(strings.TrimSpace(lookup("MODE")))
	if mode == "" {
		if botToken != "" {
			mode = "userbot+assistant"
		} else {
			mode = "userbot"
		}
	}
	taskEngine, err := loadTaskEngineConfig(lookup)
	if err != nil {
		return nil, err
	}
	durabilityConcurrency, err := optionalInt(lookup, "TASKENGINE_DURABILITY_CONCURRENCY", 4)
	if err != nil {
		return nil, err
	}
	if durabilityConcurrency <= 0 {
		return nil, fmt.Errorf("invalid TASKENGINE_DURABILITY_CONCURRENCY: must be a positive integer")
	}

	return &Config{
		AppID:                           appID,
		AppHash:                         appHash,
		Phone:                           phone,
		SessionFile:                     sessionFile,
		DatabasePath:                    databasePath,
		Prefix:                          prefix,
		OwnerID:                         ownerID,
		SudoUsers:                       sudoUsers,
		LogLevel:                        logLevel,
		BotToken:                        botToken,
		Mode:                            mode,
		TaskEngine:                      taskEngine,
		TaskEngineDurabilityConcurrency: durabilityConcurrency,
	}, nil
}

func defaultTaskEngineConfig() taskengine.Config {
	cfg := taskengine.DefaultConfig
	cfg.Pools = make(map[tasks.PoolID]taskengine.PoolEngineConfig, len(taskengine.DefaultConfig.Pools))
	for id, pool := range taskengine.DefaultConfig.Pools {
		cfg.Pools[id] = pool
	}
	cfg.ResourceCapacities = map[string]int64{"process": 2, "media": 2, "download": 3}
	return cfg
}

func taskEnginePoolEnvPrefix(id tasks.PoolID) string {
	return "TASKENGINE_POOL_" + strings.ToUpper(strings.ReplaceAll(string(id), "-", "_")) + "_"
}

func validateTaskEngineConfig(cfg taskengine.Config) error {
	if cfg.MaxTerminalRetained < 0 {
		return fmt.Errorf("taskengine: MaxTerminalRetained cannot be negative")
	}
	if err := taskengine.ValidateConfig(cfg); err != nil {
		return err
	}
	seenPrefixes := make(map[string]tasks.PoolID, len(cfg.Pools))
	for id := range cfg.Pools {
		prefix := taskEnginePoolEnvPrefix(id)
		if previous, duplicate := seenPrefixes[prefix]; duplicate {
			return fmt.Errorf("taskengine: pools %q and %q map to the same environment prefix %q", previous, id, prefix)
		}
		seenPrefixes[prefix] = id
	}
	return nil
}

func loadTaskEngineConfig(lookup func(string) string) (taskengine.Config, error) {
	cfg := defaultTaskEngineConfig()

	if rawPools := strings.TrimSpace(lookup("TASKENGINE_POOLS")); rawPools != "" {
		declared := make(map[tasks.PoolID]taskengine.PoolEngineConfig)
		for _, rawID := range strings.Split(rawPools, ",") {
			id := tasks.PoolID(strings.TrimSpace(rawID))
			if id == "" {
				return cfg, fmt.Errorf("invalid TASKENGINE_POOLS: pool name cannot be empty")
			}
			if _, duplicate := declared[id]; duplicate {
				return cfg, fmt.Errorf("invalid TASKENGINE_POOLS: duplicate pool %q", id)
			}
			declared[id] = cfg.Pools[id]
		}
		if len(declared) == 0 {
			return cfg, fmt.Errorf("invalid TASKENGINE_POOLS: at least one pool is required")
		}
		cfg.Pools = declared
	}

	poolIDs := make([]string, 0, len(cfg.Pools))
	for id := range cfg.Pools {
		poolIDs = append(poolIDs, string(id))
	}
	sort.Strings(poolIDs)
	for _, rawID := range poolIDs {
		id := tasks.PoolID(rawID)
		pool := cfg.Pools[id]
		prefix := taskEnginePoolEnvPrefix(id)
		var err error
		if pool.Concurrency, err = optionalInt(lookup, prefix+"MAX", pool.Concurrency); err != nil {
			return cfg, err
		}
		if pool.MinConcurrency, err = optionalInt(lookup, prefix+"MIN", pool.MinConcurrency); err != nil {
			return cfg, err
		}
		if pool.BacklogLimit, err = optionalInt(lookup, prefix+"BACKLOG", pool.BacklogLimit); err != nil {
			return cfg, err
		}
		if pool.PayloadBudget, err = optionalInt64(lookup, prefix+"PAYLOAD_BYTES", pool.PayloadBudget); err != nil {
			return cfg, err
		}
		if pool.IdleTimeout, err = optionalDuration(lookup, prefix+"IDLE_TIMEOUT", prefix+"IDLE_SECONDS", pool.IdleTimeout); err != nil {
			return cfg, err
		}
		cfg.Pools[id] = pool
	}

	var err error
	if cfg.ResultCapacity, err = optionalInt(lookup, "TASKENGINE_RESULT_CAPACITY", cfg.ResultCapacity); err != nil {
		return cfg, err
	}
	if cfg.MaxTerminalRetained, err = optionalInt(lookup, "TASKENGINE_MAX_TERMINAL", cfg.MaxTerminalRetained); err != nil {
		return cfg, err
	}
	if cfg.DecisionTimeout, err = optionalDuration(lookup, "TASKENGINE_DECISION_TIMEOUT", "", cfg.DecisionTimeout); err != nil {
		return cfg, err
	}
	if cfg.InboxCapacity, err = optionalInt(lookup, "TASKENGINE_INBOX_CAPACITY", cfg.InboxCapacity); err != nil {
		return cfg, err
	}
	if cfg.MaxRetainedBytes, err = optionalBytes(lookup, "TASKENGINE_MAX_RETAINED_BYTES", "TASKENGINE_RETAINED_MB", cfg.MaxRetainedBytes); err != nil {
		return cfg, err
	}
	if cfg.MaxOutputBytes, err = optionalInt64(lookup, "TASKENGINE_MAX_OUTPUT_BYTES", cfg.MaxOutputBytes); err != nil {
		return cfg, err
	}
	if cfg.MaxFailureBytes, err = optionalInt(lookup, "TASKENGINE_MAX_FAILURE_BYTES", cfg.MaxFailureBytes); err != nil {
		return cfg, err
	}
	if cfg.DeliveryConcurrency, err = optionalInt(lookup, "TASKENGINE_DELIVERY_CONCURRENCY", cfg.DeliveryConcurrency); err != nil {
		return cfg, err
	}
	if cfg.DeliveryQueueCap, err = optionalInt(lookup, "TASKENGINE_DELIVERY_QUEUE_CAP", cfg.DeliveryQueueCap); err != nil {
		return cfg, err
	}
	if cfg.TerminalTTL, err = optionalDuration(lookup, "TASKENGINE_TERMINAL_TTL", "", cfg.TerminalTTL); err != nil {
		return cfg, err
	}
	if cfg.MaxScopeTombstones, err = optionalInt(lookup, "TASKENGINE_MAX_SCOPE_TOMBSTONES", cfg.MaxScopeTombstones); err != nil {
		return cfg, err
	}

	if raw := strings.TrimSpace(lookup("TASKENGINE_RESOURCE_CAPACITIES")); raw != "" {
		cfg.ResourceCapacities = make(map[string]int64)
		if strings.EqualFold(raw, "none") {
			if err := validateTaskEngineConfig(cfg); err != nil {
				return cfg, err
			}
			return cfg, nil
		}
		for _, item := range strings.Split(raw, ",") {
			parts := strings.SplitN(strings.TrimSpace(item), "=", 2)
			name := ""
			if len(parts) == 2 {
				name = strings.TrimSpace(parts[0])
			}
			if len(parts) != 2 || name == "" {
				return cfg, fmt.Errorf("invalid TASKENGINE_RESOURCE_CAPACITIES entry %q", item)
			}
			if _, duplicate := cfg.ResourceCapacities[name]; duplicate {
				return cfg, fmt.Errorf("duplicate TASKENGINE_RESOURCE_CAPACITIES resource %q", name)
			}
			value, parseErr := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
			if parseErr != nil || value <= 0 {
				return cfg, fmt.Errorf("invalid resource capacity %q", item)
			}
			cfg.ResourceCapacities[name] = value
		}
	}
	if err := validateTaskEngineConfig(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func optionalInt(lookup func(string) string, key string, fallback int) (int, error) {
	raw := strings.TrimSpace(lookup(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid %s: must be a non-negative integer", key)
	}
	return value, nil
}

func optionalInt64(lookup func(string) string, key string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(lookup(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid %s: must be a non-negative integer", key)
	}
	return value, nil
}

func optionalBytes(lookup func(string) string, key, legacyMBKey string, fallback int64) (int64, error) {
	if raw := strings.TrimSpace(lookup(key)); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			return 0, fmt.Errorf("invalid %s: must be a non-negative integer", key)
		}
		return value, nil
	}
	if legacyMBKey == "" {
		return fallback, nil
	}
	raw := strings.TrimSpace(lookup(legacyMBKey))
	if raw == "" {
		return fallback, nil
	}
	mb, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || mb < 0 || mb > math.MaxInt64>>20 {
		return 0, fmt.Errorf("invalid %s: must be a non-negative MiB value", legacyMBKey)
	}
	return mb << 20, nil
}

func optionalDuration(lookup func(string) string, key, legacySecondsKey string, fallback time.Duration) (time.Duration, error) {
	if raw := strings.TrimSpace(lookup(key)); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value < 0 {
			return 0, fmt.Errorf("invalid %s: must be a non-negative duration", key)
		}
		return value, nil
	}
	if legacySecondsKey == "" {
		return fallback, nil
	}
	raw := strings.TrimSpace(lookup(legacySecondsKey))
	if raw == "" {
		return fallback, nil
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds < 0 || seconds > int64(math.MaxInt64/time.Second) {
		return 0, fmt.Errorf("invalid %s: must be a non-negative integer", legacySecondsKey)
	}
	return time.Duration(seconds) * time.Second, nil
}

// NormalizePhone accepts international numbers and Indonesian local numbers.
func NormalizePhone(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("PHONE is required")
	}

	var sb strings.Builder
	for i, r := range raw {
		if r == '+' && i == 0 {
			sb.WriteRune(r)
		} else if unicode.IsDigit(r) {
			sb.WriteRune(r)
		}
	}
	phone := sb.String()

	if strings.HasPrefix(phone, "08") {
		phone = "+62" + strings.TrimPrefix(phone, "0")
	} else if strings.HasPrefix(phone, "0") {
		return "", fmt.Errorf("invalid PHONE %q: local format must start with 08", raw)
	}

	if !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}

	return phone, nil
}
