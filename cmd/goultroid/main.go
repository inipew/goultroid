package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/inipew/goultroid/internal/app"
	"github.com/inipew/goultroid/internal/config"
	telegramRuntime "github.com/inipew/goultroid/internal/telegram"
	"github.com/joho/godotenv"
)

const version = "dev"

func main() {
	if err := execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		log.Printf("Error: %v", err)
		os.Exit(1)
	}
}

func execute(args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		printUsage(out)
		return nil
	}
	switch args[0] {
	case "run":
		return runCommand(args[1:], in, errOut)
	case "init":
		return initCommand(args[1:], in, out)
	case "doctor":
		return doctorCommand(args[1:], out)
	case "whoami":
		return whoamiCommand(args[1:], out)
	case "version":
		fmt.Fprintln(out, "GoUltroid", version)
		return nil
	case "help", "-h", "--help":
		printUsage(out)
		return nil
	default:
		return fmt.Errorf("unknown command %q; run 'goultroid help'", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `GoUltroid command line

Usage:
  goultroid run [-config .env]     validate config, bootstrap if absent, then run
  goultroid init [-config .env]    create configuration interactively
  goultroid doctor [-config .env]  validate configuration and local paths
  goultroid whoami [-config .env]  show the account stored in the session
  goultroid version                print version
  goultroid help                   show this help`)
}

func whoamiCommand(args []string, out io.Writer) error {
	path, err := configFlag("whoami", args)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return fmt.Errorf("configuration check failed: %w", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	account, err := telegramRuntime.WhoAmI(ctx, cfg)
	if err != nil {
		return err
	}
	kind := "user"
	if account.Bot {
		kind = "bot"
	}
	username := "-"
	if account.Username != "" {
		username = "@" + account.Username
	}
	fmt.Fprintf(out, "Authenticated as: %s\nUsername: %s\nID: %d\nPhone: %s\nType: %s\nSession: %s\n",
		account.DisplayName(), username, account.ID, maskPhone(account.Phone), kind, cfg.SessionFile)
	return nil
}

func configFlag(name string, args []string) (string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", ".env", "dotenv configuration path")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return *path, nil
}

func runCommand(args []string, in io.Reader, errOut io.Writer) error {
	path, err := configFlag("run", args)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(path)
	if err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			return fmt.Errorf("invalid config %s: %w", path, err)
		}
		fmt.Fprintf(errOut, "Config %s belum ada; memulai setup interaktif.\n", path)
		cfg, err = promptConfig(in, errOut)
		if err != nil {
			return err
		}
		if err := config.WriteEnv(path, cfg); err != nil {
			return err
		}
		fmt.Fprintf(errOut, "Config tersimpan di %s (mode 0600). Memulai login Telegram...\n", path)
	}
	return runApp(cfg)
}

func initCommand(args []string, in io.Reader, out io.Writer) error {
	path, err := configFlag("init", args)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config %s already exists; refusing to overwrite", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	cfg, err := promptConfig(in, out)
	if err != nil {
		return err
	}
	if err := config.WriteEnv(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "Config created at %s. Continue with: goultroid run -config %s\n", path, path)
	return nil
}

func doctorCommand(args []string, out io.Writer) error {
	path, err := configFlag("doctor", args)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return fmt.Errorf("configuration check failed: %w", err)
	}
	fmt.Fprintf(out, "OK config=%s phone=%s session=%s database=%s mode=%s\n", path, maskPhone(cfg.Phone), cfg.SessionFile, cfg.DatabasePath, cfg.Mode)
	return nil
}

func loadConfig(path string) (*config.Config, error) {
	if err := godotenv.Load(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return config.Load()
}

func promptConfig(in io.Reader, out io.Writer) (*config.Config, error) {
	r := bufio.NewReader(in)
	read := func(label, fallback string) (string, error) {
		if fallback == "" {
			fmt.Fprintf(out, "%s: ", label)
		} else {
			fmt.Fprintf(out, "%s [%s]: ", label, fallback)
		}
		value, err := r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			value = fallback
		}
		return value, nil
	}
	appIDRaw, err := read("Telegram APP_ID (my.telegram.org)", "")
	if err != nil {
		return nil, err
	}
	appID, err := strconv.Atoi(appIDRaw)
	if err != nil || appID <= 0 {
		return nil, fmt.Errorf("APP_ID must be a positive integer")
	}
	appHash, err := read("Telegram APP_HASH", "")
	if err != nil || appHash == "" {
		return nil, fmt.Errorf("APP_HASH is required")
	}
	phoneRaw, err := read("Phone (+628xx or 08xx)", "")
	if err != nil {
		return nil, err
	}
	phone, err := config.NormalizePhone(phoneRaw)
	if err != nil {
		return nil, err
	}
	ownerRaw, err := read("Owner Telegram user ID", "0")
	if err != nil {
		return nil, err
	}
	ownerID, err := strconv.ParseInt(ownerRaw, 10, 64)
	if err != nil || ownerID < 0 {
		return nil, fmt.Errorf("OWNER_ID must be a non-negative integer")
	}
	prefix, err := read("Command prefix", ".")
	if err != nil {
		return nil, err
	}
	botToken, err := read("Assistant BOT_TOKEN (optional)", "")
	if err != nil {
		return nil, err
	}
	mode := "userbot"
	if botToken != "" {
		mode = "userbot+assistant"
	}
	return &config.Config{AppID: appID, AppHash: appHash, Phone: phone, SessionFile: "data/session.json", DatabasePath: "data/goultroid.db", Prefix: prefix, OwnerID: ownerID, LogLevel: "info", BotToken: botToken, Mode: mode}, nil
}

func maskPhone(phone string) string {
	if len(phone) <= 5 {
		return "***"
	}
	return phone[:3] + strings.Repeat("*", len(phone)-5) + phone[len(phone)-2:]
}

func runApp(cfg *config.Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	instance, err := app.New(cfg)
	if err != nil {
		return fmt.Errorf("application initialization: %w", err)
	}
	runErr := instance.Run(ctx)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	shutdownErr := instance.Shutdown(shutdownCtx)
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf("application: %w", runErr)
	}
	if shutdownErr != nil {
		return fmt.Errorf("shutdown: %w", shutdownErr)
	}
	return nil
}
