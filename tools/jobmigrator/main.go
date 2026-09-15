package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/inipew/goultroid/internal/database"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
)

func main() {
	dbPath := flag.String("db", "data/goultroid.db", "Path to SQLite database file")
	dryRun := flag.Bool("dry-run", false, "Analyze and produce migration report without modifying database")
	validate := flag.Bool("validate", false, "Validate delta checksums and counts between legacy and new tables")
	rollback := flag.Bool("rollback", false, "Reverse project state from new schema back to legacy schema")
	flag.Parse()

	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "Error: -db path cannot be empty")
		os.Exit(1)
	}

	db, err := database.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database %q: %v\n", *dbPath, err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()
	// Ensure redesigned schema exists
	if err := jobsqlite.InitSchema(ctx, db.DB); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing jobs schema: %v\n", err)
		os.Exit(1)
	}

	migrator := jobsqlite.NewMigrator(db.DB)

	switch {
	case *validate:
		delta, err := migrator.ValidateDelta(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error validating delta: %v\n", err)
			os.Exit(1)
		}
		printJSON("Delta Validation Report", delta)
		if !delta.ChecksumMatch {
			fmt.Fprintln(os.Stderr, "WARNING: Checksum mismatch or discrepancies detected!")
			os.Exit(2)
		}
	case *rollback:
		fmt.Println("Executing reverse projection for rollback...")
		if err := migrator.ReverseProject(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Error performing reverse projection: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Reverse projection completed successfully.")
	case *dryRun:
		report, err := migrator.DryRun(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error executing dry run: %v\n", err)
			os.Exit(1)
		}
		printJSON("Dry Run Migration Report", report)
	default:
		fmt.Println("Executing transactional migration...")
		report, err := migrator.Migrate(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error executing migration: %v\n", err)
			os.Exit(1)
		}
		printJSON("Migration Execution Report", report)
	}
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
