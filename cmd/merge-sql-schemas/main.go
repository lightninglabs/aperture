package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	_ "modernc.org/sqlite" // Register the pure-Go SQLite driver.
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func run() error {
	// Open an in-memory SQLite database.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return fmt.Errorf("failed to open in-memory db: %w", err)
	}
	defer db.Close()

	migrationDir := "aperturedb/sqlc/migrations"
	files, err := os.ReadDir(migrationDir)
	if err != nil {
		return fmt.Errorf("failed to read migration dir: %w", err)
	}

	var upFiles []string
	upRegex := regexp.MustCompile(`\.up\.sql$`)
	for _, f := range files {
		if !f.IsDir() && upRegex.MatchString(f.Name()) {
			upFiles = append(upFiles, f.Name())
		}
	}
	sort.Strings(upFiles)

	// Execute each up migration in order.
	for _, fname := range upFiles {
		path := filepath.Join(migrationDir, fname)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", fname, err)
		}
		_, err = db.Exec(string(data))
		if err != nil {
			return fmt.Errorf("error executing %s: %w", fname, err)
		}
	}

	// Retrieve the final database schema from sqlite_master. After
	// running all migration files on an in-memory database, we extract
	// the schema definitions for tables, views, and indexes. Ordering
	// by name ensures the output is stable across runs. We filter where
	// sql IS NOT NULL, as internal SQLite objects have a NULL sql column.
	rows, err := db.Query(`
		SELECT type, name, sql FROM sqlite_master
		WHERE type IN ('table', 'view', 'index')
		  AND sql IS NOT NULL
		ORDER BY name`,
	)
	if err != nil {
		return fmt.Errorf("failed to query schema: %w", err)
	}
	defer rows.Close()

	var generatedSchema string
	for rows.Next() {
		var typ, name, sqlDef string
		if err := rows.Scan(&typ, &name, &sqlDef); err != nil {
			return fmt.Errorf("error scanning row: %w", err)
		}

		generatedSchema += sqlDef + ";\n\n"
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating rows: %w", err)
	}

	// Write the consolidated schema file.
	outDir := "aperturedb/sqlc/schemas"
	if err = os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("failed to create schema dir: %w", err)
	}
	outFile := filepath.Join(outDir, "generated_schema.sql")
	err = os.WriteFile(outFile, []byte(generatedSchema), 0644)
	if err != nil {
		return fmt.Errorf("failed to write schema: %w", err)
	}
	log.Printf("Consolidated schema written to %s", outFile)

	return nil
}
