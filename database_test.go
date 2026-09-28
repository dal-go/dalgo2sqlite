package dalgo2sqlite

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
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

func TestNewDatabase_OpenError(t *testing.T) {
	orig := sqlOpen
	defer func() { sqlOpen = orig }()
	sqlOpen = func(dbPath string) (*sql.DB, error) {
		return nil, errors.New("open error")
	}
	_, err := NewDatabaseWithOptions("dummy", dal.NewSchema(nil, nil), dalgo2sql.DbOptions{})
	if err == nil {
		t.Fatal("expected error from NewDatabaseWithOptions when sqlOpen fails")
	}
}

func TestDatabase_SupportsConcurrentConnections_False(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := NewDatabase(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if db.SupportsConcurrentConnections() {
		t.Error("expected SupportsConcurrentConnections() == false for SQLite, got true")
	}
}

func TestDatabase_CloseNil(t *testing.T) {
	var d Database
	if err := d.Close(); err != nil {
		t.Fatalf("Close on nil sqlDB returned error: %v", err)
	}
}

func TestDatabase_DelegatedMethods(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	schema := dal.NewSchema(nil, nil)
	db, err := NewDatabaseWithOptions(filepath.Join(dir, "methods.db"), schema, dalgo2sql.DbOptions{
		ID: "test-db-id",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if got := db.ID(); got != "test-db-id" {
		t.Errorf("ID() = %q, want %q", got, "test-db-id")
	}
	adapter := db.Adapter()
	if adapter.Name() != "dalgo2sqlite" {
		t.Errorf("Adapter().Name() = %q, want dalgo2sqlite", adapter.Name())
	}
	if adapter.Version() != Version {
		t.Errorf("Adapter().Version() = %q, want %q", adapter.Version(), Version)
	}
	if gotSchema := db.Schema(); gotSchema == nil {
		t.Errorf("Schema() returned nil")
	}
}
