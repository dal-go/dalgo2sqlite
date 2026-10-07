package dalgo2sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dbschema"
)

func TestSQLiteTypeFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   dbschema.Type
		want string
	}{
		{dbschema.Bool, "INTEGER"},
		{dbschema.Int, "INTEGER"},
		{dbschema.Float, "REAL"},
		{dbschema.String, "TEXT"},
		{dbschema.Bytes, "BLOB"},
		{dbschema.Time, "TEXT"},
		{dbschema.Decimal, "NUMERIC"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.in.String(), func(t *testing.T) {
			t.Parallel()
			got, err := sqliteTypeFor(c.in)
			if err != nil {
				t.Fatalf("sqliteTypeFor(%v): unexpected error: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("sqliteTypeFor(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSQLiteTypeFor_RejectsNull(t *testing.T) {
	t.Parallel()
	_, err := sqliteTypeFor(dbschema.Null)
	if err == nil {
		t.Error("expected error for dbschema.Null type")
	}
}

func TestDbschemaTypeFromSQLite(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in            string
		want          dbschema.Type
		ok            bool
		wantPrecision *dbschema.Precision
	}{
		{"INTEGER", dbschema.Int, true, nil},
		{"REAL", dbschema.Float, true, nil},
		{"TEXT", dbschema.String, true, nil},
		{"BLOB", dbschema.Bytes, true, nil},
		{"NUMERIC", dbschema.Decimal, true, nil},
		{"VARCHAR(255)", dbschema.String, true, nil},
		{"FLOAT", dbschema.Float, true, nil},
		{"INT", dbschema.Int, true, nil},
		{"MY_CUSTOM_TYPE", dbschema.Null, false, nil},
		// New: date/time keywords.
		{"DATETIME", dbschema.Time, true, nil},
		{"datetime", dbschema.Time, true, nil},
		{"DATE", dbschema.Time, true, nil},
		{"TIME", dbschema.Time, true, nil},
		{"BOOLEAN", dbschema.Bool, true, nil},
		{"bool", dbschema.Bool, true, nil},
		// New: NUMERIC/DECIMAL with precision.
		{"NUMERIC(10,2)", dbschema.Decimal, true, &dbschema.Precision{Total: 10, Scale: 2}},
		{"numeric(38,9)", dbschema.Decimal, true, &dbschema.Precision{Total: 38, Scale: 9}},
		{"DECIMAL(5,0)", dbschema.Decimal, true, &dbschema.Precision{Total: 5, Scale: 0}},
		{"DECIMAL(8, 3)", dbschema.Decimal, true, &dbschema.Precision{Total: 8, Scale: 3}},
		// Defensive: truly unknown type still rejected.
		{"UNKNOWN_TYPE", dbschema.Null, false, nil},
		{"JSON", dbschema.Null, false, nil},
	}
	for _, c := range cases {
		c := c
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			got, gotPrec, ok := dbschemaTypeFromSQLite(c.in)
			if ok != c.ok {
				t.Errorf("dbschemaTypeFromSQLite(%q): ok = %v, want %v", c.in, ok, c.ok)
			}
			if got != c.want {
				t.Errorf("dbschemaTypeFromSQLite(%q): got %v, want %v", c.in, got, c.want)
			}
			switch {
			case c.wantPrecision == nil && gotPrec != nil:
				t.Errorf("dbschemaTypeFromSQLite(%q): precision = %+v, want nil", c.in, gotPrec)
			case c.wantPrecision != nil && gotPrec == nil:
				t.Errorf("dbschemaTypeFromSQLite(%q): precision = nil, want %+v", c.in, c.wantPrecision)
			case c.wantPrecision != nil && gotPrec != nil && *c.wantPrecision != *gotPrec:
				t.Errorf("dbschemaTypeFromSQLite(%q): precision = %+v, want %+v", c.in, *gotPrec, *c.wantPrecision)
			}
		})
	}
}

func TestTimeMarkers_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sqlDB, err := sql.Open("sqlite", filepath.Join(dir, "markers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlDB.Close() }()
	ctx := context.Background()

	if err := ensureTimeMarkerTable(ctx, sqlDB); err != nil {
		t.Fatalf("ensureTimeMarkerTable: %v", err)
	}
	if err := writeTimeMarker(ctx, sqlDB, "events", "occurred_at"); err != nil {
		t.Fatalf("writeTimeMarker: %v", err)
	}
	if err := writeTimeMarker(ctx, sqlDB, "events", "logged_at"); err != nil {
		t.Fatalf("writeTimeMarker (second): %v", err)
	}

	got, err := readTimeMarkers(ctx, sqlDB, "events")
	if err != nil {
		t.Fatalf("readTimeMarkers: %v", err)
	}
	if len(got) != 2 || !got["occurred_at"] || !got["logged_at"] {
		t.Errorf("readTimeMarkers = %v, want {occurred_at, logged_at}", got)
	}

	// Idempotence: writing the same marker twice MUST NOT error.
	if err := writeTimeMarker(ctx, sqlDB, "events", "occurred_at"); err != nil {
		t.Errorf("writeTimeMarker idempotence: %v", err)
	}

	// Drop all markers for the collection and confirm they're gone.
	if err := dropTimeMarkers(ctx, sqlDB, "events"); err != nil {
		t.Fatalf("dropTimeMarkers: %v", err)
	}
	after, err := readTimeMarkers(ctx, sqlDB, "events")
	if err != nil {
		t.Fatalf("readTimeMarkers after drop: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("readTimeMarkers after drop = %v, want empty", after)
	}
}

func TestSqliteTypeFor_Unknown(t *testing.T) {
	t.Parallel()
	if _, err := sqliteTypeFor(dbschema.Type(127)); err == nil {
		t.Error("expected error for unknown dbschema.Type")
	}
}

func TestTimeMarkers_AbsentTable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sqlDB, err := sql.Open("sqlite", filepath.Join(dir, "nomarkers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlDB.Close() }()
	ctx := context.Background()

	if err := dropTimeMarkers(ctx, sqlDB, "events"); err != nil {
		t.Fatalf("dropTimeMarkers on absent table returned error: %v", err)
	}
}

func TestTimeMarkers_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("ensureTimeMarkerTable_error", func(t *testing.T) {
		sqlDB, _ := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.Contains(q, timeMarkerTable) {
					return nil, errors.New("ensure error"), true
				}
				return nil, nil, false
			},
		})
		if err := ensureTimeMarkerTable(ctx, sqlDB); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("writeTimeMarker_error", func(t *testing.T) {
		sqlDB, _ := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.Contains(q, "INSERT OR IGNORE INTO") {
					return nil, errors.New("insert error"), true
				}
				return nil, nil, false
			},
		})
		if err := writeTimeMarker(ctx, sqlDB, "tbl", "col"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("readTimeMarkers_probe_error", func(t *testing.T) {
		sqlDB, _ := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "SELECT name FROM sqlite_master") {
					return nil, errors.New("probe error"), true
				}
				return nil, nil, false
			},
		})
		if _, err := readTimeMarkers(ctx, sqlDB, "tbl"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("readTimeMarkers_query_error", func(t *testing.T) {
		sqlDB, _ := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "SELECT column_name FROM") {
					return nil, errors.New("query error"), true
				}
				return nil, nil, false
			},
		})
		_ = ensureTimeMarkerTable(ctx, sqlDB)
		if _, err := readTimeMarkers(ctx, sqlDB, "tbl"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("readTimeMarkers_scan_error", func(t *testing.T) {
		sqlDB, _ := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "SELECT column_name FROM") {
					return &mockRows{cols: []string{"column_name"}, rows: [][]driver.Value{{struct{}{}}}}, nil, true
				}
				return nil, nil, false
			},
		})
		_ = ensureTimeMarkerTable(ctx, sqlDB)
		if _, err := readTimeMarkers(ctx, sqlDB, "tbl"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("dropTimeMarkers_probe_error", func(t *testing.T) {
		sqlDB, _ := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "SELECT name FROM sqlite_master") {
					return nil, errors.New("probe error"), true
				}
				return nil, nil, false
			},
		})
		if err := dropTimeMarkers(ctx, sqlDB, "tbl"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("dropTimeMarkers_exec_error", func(t *testing.T) {
		sqlDB, _ := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.Contains(q, "DELETE FROM "+timeMarkerTable) {
					return nil, errors.New("delete error"), true
				}
				return nil, nil, false
			},
		})
		_ = ensureTimeMarkerTable(ctx, sqlDB)
		if err := dropTimeMarkers(ctx, sqlDB, "tbl"); err == nil {
			t.Fatal("expected error")
		}
	})
}
