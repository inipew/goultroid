package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"
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
}

// Load reads configuration from environment variables and validates all fields.
func Load() (*Config, error) {
	appIDStr := os.Getenv("APP_ID")
	if appIDStr == "" {
		return nil, fmt.Errorf("APP_ID is required")
	}
	appID, err := strconv.Atoi(appIDStr)
	if err != nil || appID <= 0 {
		return nil, fmt.Errorf("invalid APP_ID: must be a positive integer")
	}

	appHash := strings.TrimSpace(os.Getenv("APP_HASH"))
	if appHash == "" {
		return nil, fmt.Errorf("APP_HASH is required")
	}

	phone, err := NormalizePhone(os.Getenv("PHONE"))
	if err != nil {
		return nil, err
	}

	sessionFile := strings.TrimSpace(os.Getenv("SESSION_FILE"))
	if sessionFile == "" {
		sessionFile = "data/session.json"
	}

	databasePath := strings.TrimSpace(os.Getenv("DATABASE_PATH"))
	if databasePath == "" {
		databasePath = "data/goultroid.db"
	}

	prefix := os.Getenv("PREFIX")
	if prefix == "" {
		prefix = "."
	}

	var ownerID int64
	if ownerStr := strings.TrimSpace(os.Getenv("OWNER_ID")); ownerStr != "" {
		val, err := strconv.ParseInt(ownerStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid OWNER_ID: %w", err)
		}
		ownerID = val
	}

	var sudoUsers []int64
	if sudoStr := strings.TrimSpace(os.Getenv("SUDO_USERS")); sudoStr != "" {
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

	logLevel := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	if logLevel == "" {
		logLevel = "info"
	}

	botToken := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("MODE")))
	if mode == "" {
		if botToken != "" {
			mode = "userbot+assistant"
		} else {
			mode = "userbot"
		}
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
	}, nil
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
