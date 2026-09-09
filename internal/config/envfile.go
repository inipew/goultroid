package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
