package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/taskengine"
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
		execution = taskengine.DefaultConfig
	}
	for _, poolName := range []string{"general", "interactive", "download", "media-process", "scheduler"} {
		pool, ok := execution.Pools[tasks.PoolID(poolName)]
		if !ok {
			continue
		}
		prefix := "TASKENGINE_POOL_" + strings.ToUpper(strings.ReplaceAll(poolName, "-", "_")) + "_"
		values = append(values,
			[2]string{prefix + "MIN", strconv.Itoa(pool.MinConcurrency)},
			[2]string{prefix + "MAX", strconv.Itoa(pool.Concurrency)},
			[2]string{prefix + "BACKLOG", strconv.Itoa(pool.BacklogLimit)},
			[2]string{prefix + "IDLE_SECONDS", strconv.Itoa(int(pool.IdleTimeout / time.Second))},
		)
	}
	values = append(values,
		[2]string{"TASKENGINE_RETAINED_MB", strconv.FormatInt(execution.MaxRetainedBytes>>20, 10)},
		[2]string{"TASKENGINE_RESULT_CAPACITY", strconv.Itoa(execution.ResultCapacity)},
		[2]string{"TASKENGINE_MAX_TERMINAL", strconv.Itoa(execution.MaxTerminalRetained)},
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
	values = append(values, [2]string{"TASKENGINE_RESOURCE_CAPACITIES", strings.Join(resourceValues, ",")})
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
