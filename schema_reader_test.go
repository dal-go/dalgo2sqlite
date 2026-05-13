package dalgo2sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

func TestListCollections_ExcludesInternalTables(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	for _, ddlStmt := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, email TEXT)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE audit_log (id INTEGER PRIMARY KEY)`,
	} {
		if _, err := db.sqlDB.ExecContext(ctx, ddlStmt); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.ListCollections(ctx, nil)
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 collections, got %d: %v", len(got), got)
	}
	wantNames := []string{"audit_log", "orders", "users"}
	for i, want := range wantNames {
		if got[i].Name() != want {
			t.Errorf("got[%d].Name() = %q, want %q", i, got[i].Name(), want)
		}
	}
}

// openTestDB opens a fresh SQLite db in t.TempDir() and registers cleanup.
func openTestDB(t *testing.T) *Database {
	t.Helper()
	db, err := NewDatabase(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Suppress unused-import errors when later tasks remove direct refs.
var (
	_ = dal.CollectionRef{}
	_ = dbschema.CollectionDef{}
	_ = strings.Contains
)
