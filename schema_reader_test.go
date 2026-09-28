package dalgo2sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
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

// TestDescribeCollection_DatetimeAndNumericTypes asserts that
// real-world declared types like DATETIME and NUMERIC(p,s) — used by
// e.g. the Chinook sample database — are mapped to dbschema.Time and
// dbschema.Decimal respectively, instead of returning
// "unrecognized SQLite type".
func TestDescribeCollection_DatetimeAndNumericTypes(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	const create = `CREATE TABLE invoices (
		id INTEGER PRIMARY KEY,
		issued_at DATETIME,
		total NUMERIC(10,2) NOT NULL
	)`
	if _, err := db.sqlDB.ExecContext(ctx, create); err != nil {
		t.Fatal(err)
	}

	ref := dal.NewRootCollectionRef("invoices", "")
	got, err := db.DescribeCollection(ctx, &ref)
	if err != nil {
		t.Fatalf("DescribeCollection: %v", err)
	}
	if len(got.Fields) != 3 {
		t.Fatalf("Fields len = %d, want 3", len(got.Fields))
	}
	if got.Fields[1].Type != dbschema.Time {
		t.Errorf("issued_at Type = %v, want Time", got.Fields[1].Type)
	}
	if got.Fields[2].Type != dbschema.Decimal {
		t.Errorf("total Type = %v, want Decimal", got.Fields[2].Type)
	}
	if got.Fields[2].Precision == nil {
		t.Errorf("total Precision = nil, want (10,2)")
	} else if got.Fields[2].Precision.Total != 10 || got.Fields[2].Precision.Scale != 2 {
		t.Errorf("total Precision = %+v, want (10,2)", *got.Fields[2].Precision)
	}
}

func TestListCollections_ScanError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, db := newHookDB(t, &driverHooks{
		queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
			if strings.Contains(q, "sqlite_master") {
				return &mockRows{cols: []string{"name"}, rows: [][]driver.Value{{struct{}{}}}}, nil, true
			}
			return nil, nil, false
		},
	})
	if _, err := db.ListCollections(ctx, nil); err == nil {
		t.Fatal("expected scan error in ListCollections")
	}
}

func TestDescribeCollection_EdgeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	// Unrecognized SQLite type (line 113)
	if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE custom_types (id INT, weird FOOBAR)"); err != nil {
		t.Fatal(err)
	}
	ref := dal.NewRootCollectionRef("custom_types", "")
	if _, err := db.DescribeCollection(ctx, &ref); err == nil {
		t.Fatal("expected error for unrecognized SQLite type FOOBAR")
	}

	// Reverse PK order for sortPKByOrder (lines 179-181)
	if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE multi_pk (a INT, b INT, PRIMARY KEY (b, a))"); err != nil {
		t.Fatal(err)
	}
	multiRef := dal.NewRootCollectionRef("multi_pk", "")
	def, err := db.DescribeCollection(ctx, &multiRef)
	if err != nil {
		t.Fatalf("DescribeCollection: %v", err)
	}
	if len(def.PrimaryKey) != 2 || string(def.PrimaryKey[0]) != "b" || string(def.PrimaryKey[1]) != "a" {
		t.Errorf("expected PK [b, a], got %v", def.PrimaryKey)
	}

	// Origin == "pk" in ListIndexes (line 208)
	if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE pk_index_tbl (id TEXT PRIMARY KEY, val TEXT)"); err != nil {
		t.Fatal(err)
	}
	pkIdxRef := dal.NewRootCollectionRef("pk_index_tbl", "")
	indexes, err := db.ListIndexes(ctx, &pkIdxRef)
	if err != nil {
		t.Fatalf("ListIndexes: %v", err)
	}
	if len(indexes) != 0 {
		t.Errorf("expected 0 user indexes for pk_index_tbl, got %d", len(indexes))
	}
}

func TestDescribeCollection_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ref := dal.NewRootCollectionRef("users", "")

	t.Run("probe_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "SELECT name FROM sqlite_master") {
					return nil, errors.New("probe failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.DescribeCollection(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("pragma_table_info_query_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_table_info") {
					return nil, errors.New("pragma_table_info failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT)"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DescribeCollection(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("time_markers_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "SELECT column_name FROM") {
					return nil, errors.New("read time markers failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT)"); err != nil {
			t.Fatal(err)
		}
		_ = ensureTimeMarkerTable(ctx, db.sqlDB)
		if _, err := db.DescribeCollection(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("pragma_table_info_scan_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_table_info") {
					return &mockRows{
						cols: []string{"name", "type", "notnull", "dflt_value", "pk"},
						rows: [][]driver.Value{{struct{}{}}},
					}, nil, true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT)"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DescribeCollection(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("pragma_table_info_rows_err", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_table_info") {
					return &mockRows{
						cols:    []string{"name", "type", "notnull", "dflt_value", "pk"},
						nextErr: errors.New("iteration failed"),
					}, nil, true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT)"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DescribeCollection(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("list_indexes_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_index_list") {
					return nil, errors.New("index list failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT)"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DescribeCollection(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("read_foreign_keys_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "PRAGMA foreign_keys") {
					return nil, errors.New("foreign keys failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT)"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DescribeCollection(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("table_has_autoincrement_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "SELECT sql FROM sqlite_master") {
					return nil, errors.New("sql probe failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := tableHasAutoIncrement(ctx, db.sqlDB, "users"); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestListIndexes_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ref := dal.NewRootCollectionRef("users", "")

	t.Run("pragma_index_list_query_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_index_list") {
					return nil, errors.New("index list query failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.ListIndexes(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("pragma_index_list_scan_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_index_list") {
					return &mockRows{
						cols: []string{"name", "unique", "origin"},
						rows: [][]driver.Value{{struct{}{}}},
					}, nil, true
				}
				return nil, nil, false
			},
		})
		if _, err := db.ListIndexes(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("read_index_fields_query_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_index_info") {
					return nil, errors.New("index info query failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT, email TEXT)"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE INDEX ix_email ON users (email)"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ListIndexes(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("read_index_fields_scan_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_index_info") {
					return &mockRows{
						cols: []string{"name"},
						rows: [][]driver.Value{{struct{}{}}},
					}, nil, true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT, email TEXT)"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE INDEX ix_email ON users (email)"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ListIndexes(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})
}

