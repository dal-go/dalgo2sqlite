package dalgo2sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

func TestDescribeCollection_ForeignKeys(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, statement := range []string{
		`CREATE TABLE parent (a INTEGER, b INTEGER, PRIMARY KEY (a, b))`,
		`CREATE TABLE child (id INTEGER PRIMARY KEY, a INTEGER, b INTEGER,
		  FOREIGN KEY (a, b) REFERENCES parent(a, b) ON UPDATE RESTRICT ON DELETE CASCADE)`,
		`CREATE TABLE implicit_child (a INTEGER, b INTEGER,
		  FOREIGN KEY (a, b) REFERENCES parent)`,
	} {
		if _, err := db.sqlDB.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	ref := dal.NewRootCollectionRef("child", "")
	def, err := db.DescribeCollection(ctx, &ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(def.ForeignKeys) != 1 {
		t.Fatalf("foreign keys: %+v", def.ForeignKeys)
	}
	fk := def.ForeignKeys[0]
	if fk.ReferencedCollection != "parent" || len(fk.Fields) != 2 ||
		fk.Fields[0] != "a" || fk.Fields[1] != "b" ||
		len(fk.ReferencedFields) != 2 || fk.ReferencedFields[0] != "a" || fk.ReferencedFields[1] != "b" {
		t.Fatalf("composite foreign key: %+v", fk)
	}
	if fk.Enforcement != dbschema.ForeignKeyEnforcementDisabled {
		t.Fatalf("default enforcement: %s", fk.Enforcement)
	}
	if fk.OnUpdate != "RESTRICT" || fk.OnDelete != "CASCADE" {
		t.Fatalf("referential actions: %+v", fk)
	}
	if _, err := db.sqlDB.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	def, err = db.DescribeCollection(ctx, &ref)
	if err != nil {
		t.Fatal(err)
	}
	if def.ForeignKeys[0].Enforcement != dbschema.ForeignKeyEnforcementEnabled {
		t.Fatalf("enabled enforcement: %s", def.ForeignKeys[0].Enforcement)
	}
	implicit := dal.NewRootCollectionRef("implicit_child", "")
	def, err = db.DescribeCollection(ctx, &implicit)
	if err != nil {
		t.Fatal(err)
	}
	if len(def.ForeignKeys) != 1 || len(def.ForeignKeys[0].ReferencedFields) != 2 ||
		def.ForeignKeys[0].ReferencedFields[0] != "a" || def.ForeignKeys[0].ReferencedFields[1] != "b" {
		t.Fatalf("implicit target fields: %+v", def.ForeignKeys)
	}
}

func TestReadForeignKeys_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("conn_error", func(t *testing.T) {
		sqlDB, _ := newHookDB(t, &driverHooks{
			connectErr: errors.New("cannot connect"),
		})
		if _, err := readForeignKeys(ctx, sqlDB, "tbl"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("foreign_keys_pragma_error", func(t *testing.T) {
		sqlDB, _ := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "PRAGMA foreign_keys") {
					return nil, errors.New("pragma error"), true
				}
				return nil, nil, false
			},
		})
		if _, err := readForeignKeys(ctx, sqlDB, "tbl"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("foreign_key_list_query_error", func(t *testing.T) {
		sqlDB, _ := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_foreign_key_list") {
					return nil, errors.New("query error"), true
				}
				return nil, nil, false
			},
		})
		if _, err := readForeignKeys(ctx, sqlDB, "tbl"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("foreign_key_list_scan_error", func(t *testing.T) {
		sqlDB, _ := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_foreign_key_list") {
					return &mockRows{
						cols: []string{"id", "seq", "table", "from", "to", "on_update", "on_delete"},
						rows: [][]driver.Value{{struct{}{}}},
					}, nil, true
				}
				return nil, nil, false
			},
		})
		if _, err := readForeignKeys(ctx, sqlDB, "tbl"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("foreign_key_list_rows_err", func(t *testing.T) {
		sqlDB, _ := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_foreign_key_list") {
					return &mockRows{
						cols:    []string{"id", "seq", "table", "from", "to", "on_update", "on_delete"},
						nextErr: errors.New("iteration failed"),
					}, nil, true
				}
				return nil, nil, false
			},
		})
		if _, err := readForeignKeys(ctx, sqlDB, "tbl"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("target_pk_query_error", func(t *testing.T) {
		sqlDB, _ := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_table_info") {
					return nil, errors.New("pk query error"), true
				}
				return nil, nil, false
			},
		})
		if _, err := sqlDB.ExecContext(ctx, "CREATE TABLE p (id INT PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
		if _, err := sqlDB.ExecContext(ctx, "CREATE TABLE c (p_id INT, FOREIGN KEY (p_id) REFERENCES p)"); err != nil {
			t.Fatal(err)
		}
		if _, err := readForeignKeys(ctx, sqlDB, "c"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("target_pk_scan_error", func(t *testing.T) {
		sqlDB, _ := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_table_info") {
					return &mockRows{
						cols: []string{"name"},
						rows: [][]driver.Value{{struct{}{}}},
					}, nil, true
				}
				return nil, nil, false
			},
		})
		if _, err := sqlDB.ExecContext(ctx, "CREATE TABLE p (id INT PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
		if _, err := sqlDB.ExecContext(ctx, "CREATE TABLE c (p_id INT, FOREIGN KEY (p_id) REFERENCES p)"); err != nil {
			t.Fatal(err)
		}
		if _, err := readForeignKeys(ctx, sqlDB, "c"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("target_pk_rows_err", func(t *testing.T) {
		sqlDB, _ := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_table_info") {
					return &mockRows{
						cols:    []string{"name"},
						nextErr: errors.New("pk iteration failed"),
					}, nil, true
				}
				return nil, nil, false
			},
		})
		if _, err := sqlDB.ExecContext(ctx, "CREATE TABLE p (id INT PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
		if _, err := sqlDB.ExecContext(ctx, "CREATE TABLE c (p_id INT, FOREIGN KEY (p_id) REFERENCES p)"); err != nil {
			t.Fatal(err)
		}
		if _, err := readForeignKeys(ctx, sqlDB, "c"); err == nil {
			t.Fatal("expected error")
		}
	})
}

