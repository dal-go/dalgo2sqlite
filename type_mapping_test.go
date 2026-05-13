package dalgo2sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
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
		in   string
		want dbschema.Type
		ok   bool
	}{
		{"INTEGER", dbschema.Int, true},
		{"REAL", dbschema.Float, true},
		{"TEXT", dbschema.String, true},
		{"BLOB", dbschema.Bytes, true},
		{"NUMERIC", dbschema.Decimal, true},
		{"VARCHAR(255)", dbschema.String, true},
		{"FLOAT", dbschema.Float, true},
		{"INT", dbschema.Int, true},
		{"MY_CUSTOM_TYPE", dbschema.Null, false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			got, ok := dbschemaTypeFromSQLite(c.in)
			if ok != c.ok {
				t.Errorf("dbschemaTypeFromSQLite(%q): ok = %v, want %v", c.in, ok, c.ok)
			}
			if got != c.want {
				t.Errorf("dbschemaTypeFromSQLite(%q): got %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestTimeMarkers_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sqlDB, err := sql.Open("sqlite3", filepath.Join(dir, "markers.db"))
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
