package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/inipew/goultroid/internal/app"
	"github.com/inipew/goultroid/internal/config"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env file if present (optional in production)
	_ = godotenv.Load()

	// Load and validate application config
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// Trap OS interrupt signals for graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Build app components
	instance, err := app.New(cfg)
	if err != nil {
		log.Fatalf("Application initialization error: %v", err)
	}

	// Run application
	if err := instance.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Application error: %v", err)
	}

	// Graceful cleanup
	if err := instance.Shutdown(); err != nil {
		log.Printf("Shutdown warning: %v", err)
	}
}
