package dalgo2sqlite

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewDatabase_OpensFreshFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: unexpected error: %v", err)
	}
	if db == nil {
		t.Fatal("NewDatabase: returned nil db")
	}
	if _, statErr := os.Stat(dbPath); statErr != nil {
		t.Errorf("expected SQLite file to be created at %s, got stat err: %v", dbPath, statErr)
	}
}

func TestNewDatabase_RejectsNonDatabaseFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "garbage.txt")
	if err := os.WriteFile(dbPath, []byte("this is not a sqlite database"), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := NewDatabase(dbPath)
	if err == nil {
		t.Fatal("NewDatabase: expected error on malformed file, got nil")
	}
	if db != nil {
		t.Errorf("NewDatabase: expected nil db on error, got %T", db)
	}
}
