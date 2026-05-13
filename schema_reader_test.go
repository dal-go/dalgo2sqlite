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

func TestDescribeCollection_NotFound(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	got, err := db.DescribeCollection(context.Background(), &dal.CollectionRef{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got != nil {
		t.Errorf("expected nil CollectionDef on error, got %+v", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, "not found") {
		t.Errorf("expected message containing 'not found'; got: %s", msg)
	}
}

func TestDescribeCollection_BasicRoundTrip(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	const create = `CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL,
		balance NUMERIC
	)`
	if _, err := db.sqlDB.ExecContext(ctx, create); err != nil {
		t.Fatal(err)
	}

	ref := dal.NewRootCollectionRef("users", "")
	got, err := db.DescribeCollection(ctx, &ref)
	if err != nil {
		t.Fatalf("DescribeCollection: %v", err)
	}
	if got.Name != "users" {
		t.Errorf("Name = %q, want users", got.Name)
	}
	if len(got.Fields) != 3 {
		t.Fatalf("Fields len = %d, want 3", len(got.Fields))
	}
	checkField := func(idx int, wantName string, wantType dbschema.Type, wantAuto, wantNullable bool) {
		f := got.Fields[idx]
		if string(f.Name) != wantName {
			t.Errorf("Fields[%d].Name = %q, want %q", idx, f.Name, wantName)
		}
		if f.Type != wantType {
			t.Errorf("Fields[%d].Type = %v, want %v", idx, f.Type, wantType)
		}
		if f.AutoIncrement != wantAuto {
			t.Errorf("Fields[%d].AutoIncrement = %v, want %v", idx, f.AutoIncrement, wantAuto)
		}
		if f.Nullable != wantNullable {
			t.Errorf("Fields[%d].Nullable = %v, want %v", idx, f.Nullable, wantNullable)
		}
	}
	checkField(0, "id", dbschema.Int, true, false)
	checkField(1, "email", dbschema.String, false, false)
	checkField(2, "balance", dbschema.Decimal, false, true)

	if len(got.PrimaryKey) != 1 || string(got.PrimaryKey[0]) != "id" {
		t.Errorf("PrimaryKey = %v, want [id]", got.PrimaryKey)
	}
}

func TestListIndexes_ExcludesPKImplicit(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	for _, stmt := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT)`,
		`CREATE INDEX ix_users_email ON users(email)`,
		`CREATE UNIQUE INDEX uq_users_email ON users(email)`,
	} {
		if _, err := db.sqlDB.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	usersRef := dal.NewRootCollectionRef("users", "")
	got, err := db.ListIndexes(ctx, &usersRef)
	if err != nil {
		t.Fatalf("ListIndexes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 user-defined indexes (PK excluded), got %d: %+v", len(got), got)
	}
	wantNames := map[string]bool{"ix_users_email": false, "uq_users_email": true}
	for _, idx := range got {
		wantUnique, known := wantNames[idx.Name]
		if !known {
			t.Errorf("unexpected index %q", idx.Name)
			continue
		}
		if idx.Unique != wantUnique {
			t.Errorf("index %q: Unique = %v, want %v", idx.Name, idx.Unique, wantUnique)
		}
		if idx.Collection != "users" {
			t.Errorf("index %q: Collection = %q, want users", idx.Name, idx.Collection)
		}
		if len(idx.Fields) != 1 || string(idx.Fields[0]) != "email" {
			t.Errorf("index %q: Fields = %v, want [email]", idx.Name, idx.Fields)
		}
	}
}
