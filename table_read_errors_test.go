package dalgo2sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func tableReadHookDependencies(hooks *driverHooks) tableReadDependencies {
	values := map[int]int{}
	return tableReadDependencies{
		open: func(_ string, uri string) (*sql.DB, error) {
			connector, err := sqlite.NewConnector(uri)
			if err != nil {
				return nil, err
			}
			return sql.OpenDB(&hookConnector{baseConnector: connector, hooks: hooks}), nil
		},
		limit: func(_ *sql.Conn, id, next int) (int, error) {
			old := values[id]
			if next >= 0 {
				values[id] = next
			}
			return old, nil
		},
		session: dalgo2sql.NewReadSession,
	}
}

func TestTableReadOpeningFailuresCloseOwnedStorage(t *testing.T) {
	file := tableReadFixture(t, "CREATE TABLE orders(id INTEGER,value TEXT)")
	boom := errors.New("owned injected native failure")
	cases := []struct {
		name  string
		alter func(*tableReadDependencies, *driverHooks)
	}{
		{"sql-open", func(d *tableReadDependencies, _ *driverHooks) {
			d.open = func(string, string) (*sql.DB, error) { return nil, boom }
		}},
		{"connect", func(_ *tableReadDependencies, h *driverHooks) { h.connectErr = boom }},
		{"limit-set", func(d *tableReadDependencies, _ *driverHooks) {
			d.limit = func(*sql.Conn, int, int) (int, error) { return 0, boom }
		}},
		{"limit-readback", func(d *tableReadDependencies, _ *driverHooks) {
			d.limit = func(_ *sql.Conn, _ int, v int) (int, error) {
				if v < 0 {
					return 0, boom
				}
				return 0, nil
			}
		}},
		{"limit-wrong", func(d *tableReadDependencies, _ *driverHooks) {
			d.limit = func(*sql.Conn, int, int) (int, error) { return 0, nil }
		}},
		{"query-only-exec", func(_ *tableReadDependencies, h *driverHooks) {
			h.execHook = func(context.Context, string, []driver.NamedValue) (driver.Result, error, bool) {
				return nil, boom, true
			}
		}},
		{"query-only-read", func(_ *tableReadDependencies, h *driverHooks) {
			h.queryHook = func(context.Context, string, []driver.NamedValue) (driver.Rows, error, bool) { return nil, boom, true }
		}},
		{"query-only-off", func(_ *tableReadDependencies, h *driverHooks) {
			h.queryHook = func(context.Context, string, []driver.NamedValue) (driver.Rows, error, bool) {
				return &mockRows{cols: []string{"query_only"}, rows: [][]driver.Value{{int64(0)}}}, nil, true
			}
		}},
		{"begin", func(_ *tableReadDependencies, h *driverHooks) {
			h.beginHook = func(context.Context, driver.TxOptions) (driver.Tx, error, bool) { return nil, boom, true }
		}},
		{"bridge-error", func(d *tableReadDependencies, _ *driverHooks) {
			d.session = func(*sql.Tx, dalgo2sql.DbOptions) (dal.ReadSession, error) { return nil, boom }
		}},
		{"bridge-nil", func(d *tableReadDependencies, _ *driverHooks) {
			d.session = func(*sql.Tx, dalgo2sql.DbOptions) (dal.ReadSession, error) { return nil, nil }
		}},
		{"bridge-typed-nil", func(d *tableReadDependencies, _ *driverHooks) {
			d.session = func(*sql.Tx, dalgo2sql.DbOptions) (dal.ReadSession, error) {
				var session *tableValueReadSession
				return session, nil
			}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			hooks := &driverHooks{}
			deps := tableReadHookDependencies(hooks)
			test.alter(&deps, hooks)
			open := deps.open
			var db *sql.DB
			deps.open = func(driver, dsn string) (*sql.DB, error) { var err error; db, err = open(driver, dsn); return db, err }
			span, err := openTableRead(context.Background(), file, "main", "orders", TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128}, deps)
			if span != nil || err == nil {
				t.Fatalf("failed opener published span=%#v err=%v", span, err)
			}
			if db != nil && db.Stats().OpenConnections != 0 {
				t.Fatal("failed opener retained connection")
			}
			if strings.HasSuffix(test.name, "error") && !errors.Is(err, boom) {
				t.Fatalf("primary error lost: %v", err)
			}
		})
	}
	t.Run("cancel-during-bridge", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		deps := tableReadHookDependencies(&driverHooks{})
		deps.session = func(tx *sql.Tx, opts dalgo2sql.DbOptions) (dal.ReadSession, error) {
			session, err := dalgo2sql.NewReadSession(tx, opts)
			cancel()
			return session, err
		}
		if span, err := openTableRead(ctx, file, "main", "orders", TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128}, deps); span != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("expired opener=%#v,%v", span, err)
		}
	})
	t.Run("unknown-native-driver-refused", func(t *testing.T) {
		deps := tableReadHookDependencies(&driverHooks{})
		deps.limit = sqlite.Limit
		if span, err := openTableRead(context.Background(), file, "main", "orders", TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128}, deps); span != nil || err == nil {
			t.Fatal("wrapped/unknown native driver admitted")
		}
	})
}

type tableValueReadSession struct{ dal.ReadSession }

func TestTableReadMetadataFailuresAreNotDataAuthority(t *testing.T) {
	file := tableReadFixture(t, "CREATE TABLE orders(id INTEGER,value TEXT)")
	boom := errors.New("owned metadata failure")
	dbcols := []string{"seq", "name", "file"}
	tablecols := []string{"schema", "name", "type", "ncol", "wr", "strict"}
	colcols := []string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"}
	goodcol := []driver.Value{int64(0), "id", "INTEGER", int64(0), nil, int64(0), int64(0)}
	cases := []struct {
		name, query string
		rows        *mockRows
		err         error
	}{
		{"database-query", "PRAGMA database_list", nil, boom},
		{"database-scan", "PRAGMA database_list", &mockRows{cols: dbcols, rows: [][]driver.Value{{"not-an-integer", "main", file}}}, nil},
		{"database-wrong-source", "PRAGMA database_list", &mockRows{cols: dbcols, rows: [][]driver.Value{{int64(0), "main", file + ".other"}}}, nil},
		{"database-temp-shadow", "PRAGMA database_list", &mockRows{cols: dbcols, rows: [][]driver.Value{{int64(0), "main", file}, {int64(1), "temp", ""}}}, nil},
		{"database-attached", "PRAGMA database_list", &mockRows{cols: dbcols, rows: [][]driver.Value{{int64(0), "main", file}, {int64(2), "attached", file + ".other"}}}, nil},
		{"database-empty", "PRAGMA database_list", &mockRows{cols: dbcols}, nil},
		{"database-next", "PRAGMA database_list", &mockRows{cols: dbcols, nextErr: boom}, nil},
		{"table-query", `PRAGMA main.table_list("orders")`, nil, boom},
		{"table-count-mismatch", `PRAGMA main.table_list("orders")`, &mockRows{cols: tablecols, rows: [][]driver.Value{{"main", "orders", "table", int64(3), int64(0), int64(0)}}}, nil},
		{"column-query", `PRAGMA main.table_xinfo("orders")`, nil, boom},
		{"column-scan", `PRAGMA main.table_xinfo("orders")`, &mockRows{cols: colcols, rows: [][]driver.Value{{"bad", "id", "INTEGER", int64(0), nil, int64(0), int64(0)}}}, nil},
		{"column-duplicate", `PRAGMA main.table_xinfo("orders")`, &mockRows{cols: colcols, rows: [][]driver.Value{goodcol, {int64(1), "id", "INTEGER", int64(0), nil, int64(0), int64(0)}}}, nil},
		{"column-next", `PRAGMA main.table_xinfo("orders")`, &mockRows{cols: colcols, rows: [][]driver.Value{goodcol}, nextErr: boom}, nil},
		{"column-empty", `PRAGMA main.table_xinfo("orders")`, &mockRows{cols: colcols}, nil},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			hooks := &driverHooks{queryHook: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error, bool) {
				if query == test.query {
					return test.rows, test.err, true
				}
				return nil, nil, false
			}}
			deps := tableReadHookDependencies(hooks)
			if span, err := openTableRead(context.Background(), file, "main", "orders", TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128}, deps); span != nil || err == nil {
				t.Fatalf("inconsistent metadata admitted=%#v,%v", span, err)
			}
		})
	}
}

func TestTableReadFactsRemainSQLFreeDuringActiveRows(t *testing.T) {
	file := tableReadFixture(t, "CREATE TABLE orders(id INTEGER);INSERT INTO orders VALUES(1),(2)")
	dataStarted := false
	hooks := &driverHooks{queryHook: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error, bool) {
		if strings.HasPrefix(query, "SELECT ") {
			dataStarted = true
			return nil, nil, false
		}
		if dataStarted {
			return nil, errors.New("metadata SQL after data reader opened"), true
		}
		return nil, nil, false
	}}
	span, err := openTableRead(context.Background(), file, "main", "orders", TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128}, tableReadHookDependencies(hooks))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := span.Close(); err != nil {
			t.Error(err)
		}
	}()
	reader, err := span.Session().ExecuteQueryToRecordsetReader(context.Background(), dal.From(dal.NewQualifiedRootCollectionRef("main", "orders", "")).NewQuery().Limit(3).SelectIntoRecordset())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := span.Facts(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := reader.Next(); err != nil {
			t.Fatal(err)
		}
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !dataStarted {
		t.Fatal("no actual native read")
	}
}

func TestTableReadCloseReportsObservedErrorsAndInvalidatesFirst(t *testing.T) {
	boom := errors.New("observed rollback failure")
	hooks := &driverHooks{rollbackHook: func() (error, bool) { return boom, true }}
	db, _ := newHookDB(t, hooks)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	span := &TableReadSpan{ctx: context.Background(), cancel: func() {}, db: db, conn: conn, tx: tx}
	if err := span.Close(); !errors.Is(err, boom) {
		t.Fatalf("lost observed cleanup error: %v", err)
	}
	if span.Session() != nil {
		t.Fatal("failed cleanup exposed session")
	}
	if _, err := span.Facts(context.Background()); err == nil {
		t.Fatal("invalidated facts exposed")
	}
	value := tableValueReadSession{}
	if nilTableReadValue(value) {
		t.Fatal("non-nil value session refused as nil")
	}
	if !nilTableReadValue(nil) {
		t.Fatal("nil session accepted")
	}
	// Calling a zero cancellation function is safe, even after malformed construction.
	if err := (&TableReadSpan{ctx: context.Background()}).Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := sqlite.Limit(conn, sqlite3.SQLITE_LIMIT_LENGTH, -1); err == nil {
		t.Fatalf("closed Conn still usable, limit=%d", got)
	}
}

func TestTableReadDerivedLimitFailuresRefuseBeforeSession(t *testing.T) {
	file := tableReadFixture(t, "CREATE TABLE orders(a INTEGER,b INTEGER)")
	boom := errors.New("derived native bound failure")
	for _, mode := range []string{"set-error", "readback-error", "readback-mismatch", "too-small"} {
		t.Run(mode, func(t *testing.T) {
			deps := tableReadHookDependencies(&driverHooks{})
			original := deps.limit
			derived := false
			deps.limit = func(conn *sql.Conn, id, next int) (int, error) {
				if id == sqlite3.SQLITE_LIMIT_LENGTH && next == 128*1024 {
					derived = true
					if mode == "set-error" {
						return 0, boom
					}
				}
				if derived && id == sqlite3.SQLITE_LIMIT_LENGTH && next < 0 {
					switch mode {
					case "readback-error":
						return 0, boom
					case "readback-mismatch":
						return 1, nil
					}
				}
				return original(conn, id, next)
			}
			calls := 0
			deps.session = func(*sql.Tx, dalgo2sql.DbOptions) (dal.ReadSession, error) {
				calls++
				t.Error("failed native cap reached session factory")
				return nil, nil
			}
			limits := TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128}
			if mode == "too-small" {
				limits.MaxBytes = 15
			}
			if span, err := openTableRead(context.Background(), file, "main", "orders", limits, deps); span != nil || err == nil || calls != 0 {
				t.Fatalf("bad cap admitted=%#v,%v,calls%d", span, err, calls)
			}
		})
	}
}
