package dalgo2sqlite

import (
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
