package dalgo2sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
)

func sourceHookDB(t *testing.T) (*Database, *driverHooks) {
	t.Helper()
	hooks := &driverHooks{}
	sqlDB, db := newHookDB(t, hooks)
	for _, statement := range []string{
		`CREATE TABLE source (id INTEGER PRIMARY KEY, amount NUMERIC, note TEXT)`,
		`CREATE INDEX source_amount ON source(amount)`,
		`CREATE VIEW source_view AS SELECT note FROM source`,
		`INSERT INTO source VALUES (1, 1.25, 'first')`,
	} {
		if _, err := sqlDB.ExecContext(context.Background(), statement); err != nil {
			t.Fatal(err)
		}
	}
	return db, hooks
}

func TestSourceConstraintsPropagateSQLiteFailures(t *testing.T) {
	for _, stage := range []struct {
		name, query, mode string
	}{
		{"state-query", `PRAGMA ignore_check_constraints`, "query"},
		{"state-scan", `PRAGMA ignore_check_constraints`, "scan"},
		{"integrity-query", `PRAGMA integrity_check`, "query"},
		{"integrity-scan", `PRAGMA integrity_check`, "scan"},
		{"integrity-rows", `PRAGMA integrity_check`, "rows"},
		{"foreign-key-query", `PRAGMA foreign_key_check`, "query"},
		{"foreign-key-scan", `PRAGMA foreign_key_check`, "scan"},
		{"foreign-key-rows", `PRAGMA foreign_key_check`, "rows"},
	} {
		t.Run(stage.name, func(t *testing.T) {
			hooks := &driverHooks{}
			_, db := newHookDB(t, hooks)
			failQueryAt(hooks, stage.query, stage.mode)
			if err := db.CheckSourceConstraints(context.Background()); err == nil {
				t.Fatal("SQLite constraint-check failure was ignored")
			}
		})
	}
	t.Run("connection", func(t *testing.T) {
		hooks := &driverHooks{connectErr: errors.New("unavailable")}
		_, db := newHookDB(t, hooks)
		if err := db.CheckSourceConstraints(context.Background()); err == nil {
			t.Fatal("connection failure was ignored")
		}
	})
	t.Run("integrity-empty", func(t *testing.T) {
		hooks := &driverHooks{}
		_, db := newHookDB(t, hooks)
		hooks.queryHook = func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error, bool) {
			if query == `PRAGMA integrity_check` {
				return &mockRows{cols: []string{"integrity_check"}}, nil, true
			}
			return nil, nil, false
		}
		if err := db.CheckSourceConstraints(context.Background()); err == nil {
			t.Fatal("missing integrity result was ignored")
		}
	})
	for _, stage := range []struct{ name, statement string }{
		{"disable-check-ignoring", `PRAGMA ignore_check_constraints=OFF`},
		{"restore-check-ignoring", `PRAGMA ignore_check_constraints=ON`},
	} {
		t.Run(stage.name, func(t *testing.T) {
			hooks := &driverHooks{}
			sqlDB, db := newHookDB(t, hooks)
			if _, err := sqlDB.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			hooks.execHook = func(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error, bool) {
				if query == stage.statement {
					return nil, errors.New("injected pragma failure"), true
				}
				return nil, nil, false
			}
			if err := db.CheckSourceConstraints(context.Background()); err == nil {
				t.Fatal("CHECK enforcement state failure was ignored")
			}
		})
	}
}

func failQueryAt(hooks *driverHooks, snippet, mode string) {
	hooks.queryHook = func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error, bool) {
		if !strings.Contains(query, snippet) {
			return nil, nil, false
		}
		switch mode {
		case "query":
			return nil, errors.New("injected query failure"), true
		case "scan":
			return &mockRows{cols: []string{"bad"}, rows: [][]driver.Value{{struct{}{}}}}, nil, true
		case "rows":
			return &mockRows{cols: []string{"bad"}, nextErr: errors.New("injected iteration failure")}, nil, true
		default:
			panic("unknown fault mode")
		}
	}
}

func TestSourceDefinitionPropagatesCatalogFaults(t *testing.T) {
	stages := []struct {
		name, sql string
		modes     []string
	}{
		{"hidden", `SELECT name, hidden FROM pragma_table_xinfo`, []string{"query", "scan", "rows"}},
		{"tableDDL", `SELECT sql FROM sqlite_master WHERE type='table'`, []string{"query", "scan"}},
		{"columns", `SELECT name, type, "notnull"`, []string{"query", "scan", "rows"}},
		{"indexes", `SELECT name, "unique", origin, partial`, []string{"query", "scan", "rows"}},
		{"indexDDL", `SELECT sql FROM sqlite_master WHERE type='index'`, []string{"query", "scan"}},
		{"terms", `SELECT seqno, name, "desc"`, []string{"query", "scan", "rows"}},
	}
	for _, stage := range stages {
		for _, mode := range stage.modes {
			t.Run(stage.name+"/"+mode, func(t *testing.T) {
				db, hooks := sourceHookDB(t)
				failQueryAt(hooks, stage.sql, mode)
				if _, err := readSourceDefinition(context.Background(), db.sqlDB, "source"); err == nil {
					t.Fatal("source catalog fault was ignored")
				}
			})
		}
	}
}

func TestSourceViewsPropagateCatalogFaults(t *testing.T) {
	for _, stage := range []struct{ name, sql string }{
		{"views", `SELECT name, sql FROM sqlite_master WHERE type='view'`},
		{"columns", `SELECT name FROM pragma_table_info`},
	} {
		for _, mode := range []string{"query", "scan", "rows"} {
			t.Run(stage.name+"/"+mode, func(t *testing.T) {
				db, hooks := sourceHookDB(t)
				failQueryAt(hooks, stage.sql, mode)
				if _, err := db.ListSourceViews(context.Background()); err == nil {
					t.Fatal("view catalog fault was ignored")
				}
			})
		}
	}
}

func TestSourceRowsPropagateReadFaults(t *testing.T) {
	t.Run("nil-reference", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{})
		if _, err := db.OpenSourceRows(context.Background(), nil); err == nil {
			t.Fatal("nil source reference was accepted")
		}
	})
	t.Run("missing-table", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{})
		ref := dal.NewRootCollectionRef("missing", "")
		if _, err := db.OpenSourceRows(context.Background(), &ref); err == nil {
			t.Fatal("missing source table was accepted")
		}
	})
	for _, mode := range []string{"query", "scan", "rows"} {
		t.Run("columns/"+mode, func(t *testing.T) {
			db, hooks := sourceHookDB(t)
			failQueryAt(hooks, `SELECT name, type, pk FROM pragma_table_info`, mode)
			ref := dal.NewRootCollectionRef("source", "")
			if _, err := db.OpenSourceRows(context.Background(), &ref); err == nil {
				t.Fatal("source column fault was ignored")
			}
		})
	}
	for _, mode := range []string{"query", "scan", "rows"} {
		t.Run("records/"+mode, func(t *testing.T) {
			db, hooks := sourceHookDB(t)
			failQueryAt(hooks, `SELECT CASE WHEN typeof(`, mode)
			ref := dal.NewRootCollectionRef("source", "")
			cursor, err := db.OpenSourceRows(context.Background(), &ref)
			if mode == "query" {
				if err == nil {
					t.Fatal("source query fault was ignored")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cursor.Close() }()
			if _, err := cursor.Next(); err == nil {
				t.Fatal("source row fault was ignored")
			}
		})
	}
	for _, stage := range []struct {
		name string
		row  []driver.Value
	}{
		{"invalid-storage-class", []driver.Value{int64(1), float64(1.25), "first", "integer", int64(5), "text"}},
		{"non-finite-decimal", []driver.Value{int64(1), math.NaN(), "first", "integer", "real", "text"}},
	} {
		t.Run(stage.name, func(t *testing.T) {
			db, hooks := sourceHookDB(t)
			hooks.queryHook = func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.HasPrefix(query, `SELECT CASE WHEN typeof(`) {
					return &mockRows{cols: []string{"id", "amount", "note", "class-id", "class-amount", "class-note"}, rows: [][]driver.Value{stage.row}}, nil, true
				}
				return nil, nil, false
			}
			ref := dal.NewRootCollectionRef("source", "")
			cursor, err := db.OpenSourceRows(context.Background(), &ref)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cursor.Close() }()
			if _, err := cursor.Next(); err == nil {
				t.Fatal("invalid source value was accepted")
			}
		})
	}
}

func TestSourceReaderRejectsUnstableCatalogueAndKeylessOrdering(t *testing.T) {
	t.Run("catalogue-columns-change", func(t *testing.T) {
		db, hooks := sourceHookDB(t)
		hooks.queryHook = func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error, bool) {
			if query == `SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?) ORDER BY cid` {
				return &mockRows{cols: []string{"name", "type", "notnull", "dflt_value", "pk"}, rows: [][]driver.Value{
					{"changed_id", "INTEGER", int64(0), nil, int64(1)},
					{"amount", "NUMERIC", int64(0), nil, int64(0)},
					{"note", "TEXT", int64(0), nil, int64(0)},
				}}, nil, true
			}
			return nil, nil, false
		}
		ref := dal.NewRootCollectionRef("source", "")
		if _, err := db.DescribeCollection(context.Background(), &ref); err == nil || !strings.Contains(err.Error(), "source columns differ") {
			t.Fatalf("catalogue drift must fail: %v", err)
		}
	})
	t.Run("index-terms-fail", func(t *testing.T) {
		db, hooks := sourceHookDB(t)
		failQueryAt(hooks, `SELECT name FROM pragma_index_info`, "rows")
		if _, err := readIndexFields(context.Background(), db.sqlDB, "source_amount"); err == nil {
			t.Fatal("index iteration failure was ignored")
		}
	})
	t.Run("all-rowid-aliases-shadowed", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{})
		if _, err := db.sqlDB.Exec(`CREATE TABLE aliases (_rowid_ TEXT, rowid TEXT, oid TEXT)`); err != nil {
			t.Fatal(err)
		}
		ref := dal.NewRootCollectionRef("aliases", "")
		if _, err := db.OpenSourceRows(context.Background(), &ref); err == nil || !strings.Contains(err.Error(), "shadows every hidden rowid alias") {
			t.Fatalf("keyless order must fail closed: %v", err)
		}
	})
}
