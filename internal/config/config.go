package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

// Config holds all configuration values for the application.
type Config struct {
	AppID        int
	AppHash      string
	Phone        string
	SessionFile  string
	DatabasePath string
	Prefix       string
	OwnerID      int64
	SudoUsers    []int64
	LogLevel     string
	BotToken     string
	Mode         string
	TaskEngine   taskengine.Config
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

	return &Config{
		AppID:        appID,
		AppHash:      appHash,
		Phone:        phone,
		SessionFile:  sessionFile,
		DatabasePath: databasePath,
		Prefix:       prefix,
		OwnerID:      ownerID,
		SudoUsers:    sudoUsers,
		LogLevel:     logLevel,
		BotToken:     botToken,
		Mode:         mode,
		TaskEngine:   taskEngine,
	}, nil
}

func loadTaskEngineConfig(lookup func(string) string) (taskengine.Config, error) {
	cfg := taskengine.DefaultConfig
	cfg.Pools = make(map[tasks.PoolID]taskengine.PoolEngineConfig, len(taskengine.DefaultConfig.Pools))
	for id, pool := range taskengine.DefaultConfig.Pools {
		cfg.Pools[id] = pool
	}
	cfg.ResourceCapacities = map[string]int64{"process": 2, "media": 2, "download": 3}
	for id, pool := range cfg.Pools {
		prefix := "TASKENGINE_POOL_" + strings.ToUpper(strings.ReplaceAll(string(id), "-", "_")) + "_"
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
		idleSeconds, err := optionalInt(lookup, prefix+"IDLE_SECONDS", int(pool.IdleTimeout/time.Second))
		if err != nil {
			return cfg, err
		}
		pool.IdleTimeout = time.Duration(idleSeconds) * time.Second
		cfg.Pools[id] = pool
	}
	var err error
	if cfg.ResultCapacity, err = optionalInt(lookup, "TASKENGINE_RESULT_CAPACITY", cfg.ResultCapacity); err != nil {
		return cfg, err
	}
	if cfg.MaxTerminalRetained, err = optionalInt(lookup, "TASKENGINE_MAX_TERMINAL", cfg.MaxTerminalRetained); err != nil {
		return cfg, err
	}
	retainedMB, err := optionalInt(lookup, "TASKENGINE_RETAINED_MB", int(cfg.MaxRetainedBytes>>20))
	if err != nil {
		return cfg, err
	}
	cfg.MaxRetainedBytes = int64(retainedMB) << 20
	if raw := strings.TrimSpace(lookup("TASKENGINE_RESOURCE_CAPACITIES")); raw != "" {
		cfg.ResourceCapacities = make(map[string]int64)
		for _, item := range strings.Split(raw, ",") {
			parts := strings.SplitN(strings.TrimSpace(item), "=", 2)
			if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
				return cfg, fmt.Errorf("invalid TASKENGINE_RESOURCE_CAPACITIES entry %q", item)
			}
			value, parseErr := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
			if parseErr != nil || value <= 0 {
				return cfg, fmt.Errorf("invalid resource capacity %q", item)
			}
			cfg.ResourceCapacities[strings.TrimSpace(parts[0])] = value
		}
	}
	if err := taskengine.ValidateConfig(cfg); err != nil {
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
