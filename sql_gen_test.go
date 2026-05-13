package dalgo2sqlite

import (
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
)

func TestBuildCreateTableSQL_Simple(t *testing.T) {
	t.Parallel()
	c := dbschema.CollectionDef{
		Name: "users",
		Fields: []dbschema.FieldDef{
			{Name: dal.FieldName("id"), Type: dbschema.Int, AutoIncrement: true},
			{Name: dal.FieldName("email"), Type: dbschema.String, Nullable: false},
			{Name: dal.FieldName("balance"), Type: dbschema.Decimal, Nullable: true},
		},
		PrimaryKey: []dal.FieldName{"id"},
	}
	got, err := buildCreateTableSQL(c, ddl.Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, email TEXT NOT NULL, balance NUMERIC)"
	if got != want {
		t.Errorf("buildCreateTableSQL mismatch.\n  got:  %s\n  want: %s", got, want)
	}
}

func TestBuildCreateTableSQL_IfNotExists(t *testing.T) {
	t.Parallel()
	c := dbschema.CollectionDef{
		Name:   "users",
		Fields: []dbschema.FieldDef{{Name: dal.FieldName("id"), Type: dbschema.Int}},
	}
	got, err := buildCreateTableSQL(c, ddl.ResolveOptions(ddl.IfNotExists()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, "CREATE TABLE IF NOT EXISTS ") {
		t.Errorf("expected IF NOT EXISTS prefix; got: %s", got)
	}
}

func TestBuildCreateTableSQL_CompositePK(t *testing.T) {
	t.Parallel()
	c := dbschema.CollectionDef{
		Name: "order_lines",
		Fields: []dbschema.FieldDef{
			{Name: dal.FieldName("order_id"), Type: dbschema.Int, Nullable: false},
			{Name: dal.FieldName("line_no"), Type: dbschema.Int, Nullable: false},
			{Name: dal.FieldName("qty"), Type: dbschema.Int},
		},
		PrimaryKey: []dal.FieldName{"order_id", "line_no"},
	}
	got, err := buildCreateTableSQL(c, ddl.Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "CREATE TABLE order_lines (order_id INTEGER NOT NULL, line_no INTEGER NOT NULL, qty INTEGER, PRIMARY KEY (order_id, line_no))"
	if got != want {
		t.Errorf("buildCreateTableSQL composite-pk mismatch.\n  got:  %s\n  want: %s", got, want)
	}
}

func TestBuildCreateTableSQL_RejectsNullType(t *testing.T) {
	t.Parallel()
	c := dbschema.CollectionDef{
		Name: "users",
		Fields: []dbschema.FieldDef{
			{Name: dal.FieldName("id"), Type: dbschema.Int},
			{Name: dal.FieldName("bad"), Type: dbschema.Null},
		},
	}
	_, err := buildCreateTableSQL(c, ddl.Options{})
	if err == nil {
		t.Fatal("expected error for Null type, got nil")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("expected error to name the offending field 'bad'; got: %s", err)
	}
}

func TestBuildCreateIndexSQL(t *testing.T) {
	t.Parallel()
	idx := dbschema.IndexDef{
		Name:       "ix_users_email",
		Collection: "users",
		Fields:     []dal.FieldName{"email"},
		Unique:     false,
	}
	got, err := buildCreateIndexSQL(idx, ddl.Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "CREATE INDEX ix_users_email ON users (email)"
	if got != want {
		t.Errorf("buildCreateIndexSQL mismatch.\n  got:  %s\n  want: %s", got, want)
	}
}

func TestBuildCreateIndexSQL_Unique(t *testing.T) {
	t.Parallel()
	idx := dbschema.IndexDef{
		Name:       "uq_users_email",
		Collection: "users",
		Fields:     []dal.FieldName{"email"},
		Unique:     true,
	}
	got, err := buildCreateIndexSQL(idx, ddl.Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "CREATE UNIQUE INDEX uq_users_email ON users (email)"
	if got != want {
		t.Errorf("buildCreateIndexSQL unique mismatch.\n  got:  %s\n  want: %s", got, want)
	}
}

func TestBuildDropTableSQL(t *testing.T) {
	t.Parallel()
	got := buildDropTableSQL("users", ddl.Options{})
	want := "DROP TABLE users"
	if got != want {
		t.Errorf("buildDropTableSQL mismatch: got %q, want %q", got, want)
	}
	gotIf := buildDropTableSQL("users", ddl.ResolveOptions(ddl.IfExists()))
	wantIf := "DROP TABLE IF EXISTS users"
	if gotIf != wantIf {
		t.Errorf("buildDropTableSQL IfExists mismatch: got %q, want %q", gotIf, wantIf)
	}
}

func TestBuildAlterTableAddColumn(t *testing.T) {
	t.Parallel()
	f := dbschema.FieldDef{Name: dal.FieldName("age"), Type: dbschema.Int, Nullable: true}
	got, err := buildAlterTableAddColumnSQL("users", f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "ALTER TABLE users ADD COLUMN age INTEGER"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildAlterTableDropColumn(t *testing.T) {
	t.Parallel()
	got := buildAlterTableDropColumnSQL("users", dal.FieldName("age"))
	want := "ALTER TABLE users DROP COLUMN age"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildAlterTableRenameColumn(t *testing.T) {
	t.Parallel()
	got := buildAlterTableRenameColumnSQL("users", dal.FieldName("email"), dal.FieldName("email_address"))
	want := "ALTER TABLE users RENAME COLUMN email TO email_address"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildDropIndexSQL(t *testing.T) {
	t.Parallel()
	got := buildDropIndexSQL("ix_users_email", ddl.Options{})
	want := "DROP INDEX ix_users_email"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	gotIf := buildDropIndexSQL("ix_users_email", ddl.ResolveOptions(ddl.IfExists()))
	wantIf := "DROP INDEX IF EXISTS ix_users_email"
	if gotIf != wantIf {
		t.Errorf("got %q, want %q", gotIf, wantIf)
	}
}
