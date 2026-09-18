package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/inipew/goultroid/internal/database"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
)

var errValidationMismatch = errors.New("migration validation mismatch")

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errValidationMismatch) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run() error {
	dbPath := flag.String("db", "data/goultroid.db", "Path to SQLite database file")
	dryRun := flag.Bool("dry-run", false, "Analyze and produce migration report without modifying database")
	validate := flag.Bool("validate", false, "Validate delta checksums and counts between legacy and new tables")
	rollback := flag.Bool("rollback", false, "Reverse project state from new schema back to legacy schema")
	activate := flag.Bool("activate", false, "Activate the redesigned scheduler after successful validation")
	flag.Parse()

	if *dbPath == "" {
		return errors.New("-db path cannot be empty")
	}

	db, err := database.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open database %q: %w", *dbPath, err)
	}
	defer db.Close()

	ctx := context.Background()
	// A dry run is strictly read-only, including schema creation. The migrator
	// tolerates an absent mapping table and reports every legacy row as unmapped.
	if !*dryRun {
		if err := jobsqlite.InitSchema(ctx, db.DB); err != nil {
			return fmt.Errorf("initialize jobs schema: %w", err)
		}
	}

	migrator := jobsqlite.NewMigrator(db.DB)
	switch {
	case *activate:
		if err := migrator.ActivateCutover(ctx); err != nil {
			return fmt.Errorf("activate cutover: %w", err)
		}
		fmt.Println("Redesigned execution cutover activated.")
	case *validate:
		delta, err := migrator.ValidateDelta(ctx)
		if err != nil {
			return fmt.Errorf("validate delta: %w", err)
		}
		printJSON("Delta Validation Report", delta)
		if !delta.ChecksumMatch {
			return fmt.Errorf("%w: checksum mismatch or discrepancies detected", errValidationMismatch)
		}
	case *rollback:
		fmt.Println("Executing reverse projection for rollback...")
		if err := migrator.ReverseProject(ctx); err != nil {
			return fmt.Errorf("perform reverse projection: %w", err)
		}
		fmt.Println("Reverse projection completed successfully.")
	case *dryRun:
		report, err := migrator.DryRun(ctx)
		if err != nil {
			return fmt.Errorf("execute dry run: %w", err)
		}
		printJSON("Dry Run Migration Report", report)
	default:
		fmt.Println("Executing transactional migration...")
		report, err := migrator.Migrate(ctx)
		if err != nil {
			return fmt.Errorf("execute migration: %w", err)
		}
		printJSON("Migration Execution Report", report)
	}
	return nil
}

func printJSON(title string, v any) {
	fmt.Printf("=== %s ===\n", title)
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Printf("Error formatting JSON: %v\n", err)
		return
	}
	fmt.Println(string(out))
}
