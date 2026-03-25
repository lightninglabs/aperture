package main

import (
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	_ "modernc.org/sqlite" // Register the pure-Go SQLite driver.
)

func main() {
	// Open an in-memory SQLite database.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatalf("Failed to open in-memory db: %v", err)
	}
	defer db.Close()

	migrationDir := "aperturedb/sqlc/migrations"
	files, err := os.ReadDir(migrationDir)
	if err != nil {
		log.Fatalf("Failed to read migration dir: %v", err)
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
			log.Fatalf("Failed to read file %s: %v", fname, err)
		}
		_, err = db.Exec(string(data))
		if err != nil {
			log.Fatalf("Error executing migration %s: %v",
				fname, err)
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
		log.Fatalf("Failed to query schema: %v", err)
	}
	defer rows.Close()

	var generatedSchema string
	for rows.Next() {
		var typ, name, sqlDef string
		if err := rows.Scan(&typ, &name, &sqlDef); err != nil {
			log.Fatalf("Error scanning row: %v", err)
		}

		generatedSchema += sqlDef + ";\n\n"
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("Error iterating rows: %v", err)
	}

	// Write the consolidated schema file.
	outDir := "aperturedb/sqlc/schemas"
	if err = os.MkdirAll(outDir, 0755); err != nil {
		log.Fatalf("Failed to create schema output dir: %v", err)
	}
	outFile := filepath.Join(outDir, "generated_schema.sql")
	err = os.WriteFile(outFile, []byte(generatedSchema), 0644)
	if err != nil {
		log.Fatalf("Failed to write schema file: %v", err)
	}
	log.Printf("Consolidated schema written to %s", outFile)
}
