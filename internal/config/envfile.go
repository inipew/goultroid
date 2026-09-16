package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/inipew/goultroid/internal/tasks"
)

// WriteEnv writes a complete dotenv configuration without exposing session data.
func WriteEnv(path string, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	values := [][2]string{
		{"APP_ID", strconv.Itoa(cfg.AppID)}, {"APP_HASH", cfg.AppHash}, {"PHONE", cfg.Phone},
		{"SESSION_FILE", cfg.SessionFile}, {"DATABASE_PATH", cfg.DatabasePath}, {"PREFIX", cfg.Prefix},
		{"OWNER_ID", strconv.FormatInt(cfg.OwnerID, 10)}, {"SUDO_USERS", joinIDs(cfg.SudoUsers)},
		{"BOT_TOKEN", cfg.BotToken}, {"MODE", cfg.Mode}, {"LOG_LEVEL", cfg.LogLevel},
	}

	execution := cfg.TaskEngine
	if len(execution.Pools) == 0 {
		execution = defaultTaskEngineConfig()
	}
	if err := validateTaskEngineConfig(execution); err != nil {
		return fmt.Errorf("invalid taskengine config: %w", err)
	}

	poolNames := make([]string, 0, len(execution.Pools))
	for id := range execution.Pools {
		poolNames = append(poolNames, string(id))
	}
	sort.Strings(poolNames)
	values = append(values, [2]string{"TASKENGINE_POOLS", strings.Join(poolNames, ",")})
	for _, poolName := range poolNames {
		pool := execution.Pools[tasks.PoolID(poolName)]
		prefix := taskEnginePoolEnvPrefix(tasks.PoolID(poolName))
		values = append(values,
			[2]string{prefix + "MIN", strconv.Itoa(pool.MinConcurrency)},
			[2]string{prefix + "MAX", strconv.Itoa(pool.Concurrency)},
			[2]string{prefix + "BACKLOG", strconv.Itoa(pool.BacklogLimit)},
			[2]string{prefix + "PAYLOAD_BYTES", strconv.FormatInt(pool.PayloadBudget, 10)},
			[2]string{prefix + "IDLE_TIMEOUT", pool.IdleTimeout.String()},
		)
	}

	values = append(values,
		[2]string{"TASKENGINE_RESULT_CAPACITY", strconv.Itoa(execution.ResultCapacity)},
		[2]string{"TASKENGINE_MAX_TERMINAL", strconv.Itoa(execution.MaxTerminalRetained)},
		[2]string{"TASKENGINE_DECISION_TIMEOUT", execution.DecisionTimeout.String()},
		[2]string{"TASKENGINE_INBOX_CAPACITY", strconv.Itoa(execution.InboxCapacity)},
		[2]string{"TASKENGINE_MAX_RETAINED_BYTES", strconv.FormatInt(execution.MaxRetainedBytes, 10)},
		[2]string{"TASKENGINE_MAX_OUTPUT_BYTES", strconv.FormatInt(execution.MaxOutputBytes, 10)},
		[2]string{"TASKENGINE_MAX_FAILURE_BYTES", strconv.Itoa(execution.MaxFailureBytes)},
		[2]string{"TASKENGINE_DELIVERY_CONCURRENCY", strconv.Itoa(execution.DeliveryConcurrency)},
		[2]string{"TASKENGINE_DELIVERY_QUEUE_CAP", strconv.Itoa(execution.DeliveryQueueCap)},
		[2]string{"TASKENGINE_TERMINAL_TTL", execution.TerminalTTL.String()},
		[2]string{"TASKENGINE_MAX_SCOPE_TOMBSTONES", strconv.Itoa(execution.MaxScopeTombstones)},
	)

	resourceNames := make([]string, 0, len(execution.ResourceCapacities))
	for name := range execution.ResourceCapacities {
		resourceNames = append(resourceNames, name)
	}
	sort.Strings(resourceNames)
	resourceValues := make([]string, 0, len(resourceNames))
	for _, name := range resourceNames {
		resourceValues = append(resourceValues, name+"="+strconv.FormatInt(execution.ResourceCapacities[name], 10))
	}
	resourceConfig := strings.Join(resourceValues, ",")
	if len(resourceValues) == 0 {
		resourceConfig = "none"
	}
	durabilityConcurrency := cfg.TaskEngineDurabilityConcurrency
	if durabilityConcurrency == 0 {
		durabilityConcurrency = 4
	}
	if durabilityConcurrency < 0 {
		return fmt.Errorf("invalid TASKENGINE_DURABILITY_CONCURRENCY: must be a positive integer")
	}
	values = append(values,
		[2]string{"TASKENGINE_RESOURCE_CAPACITIES", resourceConfig},
		[2]string{"TASKENGINE_DURABILITY_CONCURRENCY", strconv.Itoa(durabilityConcurrency)},
	)

	var content strings.Builder
	for _, item := range values {
		if strings.ContainsAny(item[1], "\r\n") {
			return fmt.Errorf("invalid newline in %s", item[0])
		}
		fmt.Fprintf(&content, "%s=%s\n", item[0], item[1])
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create config directory: %w", err)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content.String()), 0600); err != nil {
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := os.Chmod(tmp, 0600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("secure temporary config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install config: %w", err)
	}
	return nil
}

func joinIDs(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ",")
}
