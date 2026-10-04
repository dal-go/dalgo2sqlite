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
	want := `CREATE TABLE "users" ("id" INTEGER PRIMARY KEY AUTOINCREMENT, "email" TEXT NOT NULL, "balance" NUMERIC)`
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
	want := `CREATE TABLE "order_lines" ("order_id" INTEGER NOT NULL, "line_no" INTEGER NOT NULL, "qty" INTEGER, PRIMARY KEY ("order_id", "line_no"))`
	if got != want {
		t.Errorf("buildCreateTableSQL composite-pk mismatch.\n  got:  %s\n  want: %s", got, want)
	}
}

func TestGeneratedDDLQuotesNativeIdentifiers(t *testing.T) {
	t.Parallel()
	db := openClassifierTestDB(t)

	const tableName = `Product "ProductPhoto"`
	const quotedColumn = `O"Brien`
	collection := dbschema.CollectionDef{
		Name: tableName,
		Fields: []dbschema.FieldDef{
			{Name: dal.FieldName("Primary"), Type: dbschema.Int, AutoIncrement: true},
			{Name: dal.FieldName(quotedColumn), Type: dbschema.String, Nullable: false},
			{Name: dal.FieldName("display name"), Type: dbschema.String, Nullable: true},
		},
		PrimaryKey: []dal.FieldName{"Primary"},
	}
	createTable, err := buildCreateTableSQL(collection, ddl.Options{})
	if err != nil {
		t.Fatalf("buildCreateTableSQL: %v", err)
	}
	if _, err = db.Exec(createTable); err != nil {
		t.Fatalf("execute generated CREATE TABLE: %v\nSQL: %s", err, createTable)
	}

	createIndex, err := buildCreateIndexSQL(dbschema.IndexDef{
		Name:       `idx "display"`,
		Collection: tableName,
		Fields:     []dal.FieldName{"display name"},
	}, ddl.Options{})
	if err != nil {
		t.Fatalf("buildCreateIndexSQL: %v", err)
	}
	if _, err = db.Exec(createIndex); err != nil {
		t.Fatalf("execute generated CREATE INDEX: %v\nSQL: %s", err, createIndex)
	}

	addColumn, err := buildAlterTableAddColumnSQL(tableName, dbschema.FieldDef{
		Name: dal.FieldName(`added "column"`), Type: dbschema.String, Nullable: true,
	})
	if err != nil {
		t.Fatalf("buildAlterTableAddColumnSQL: %v", err)
	}
	if _, err = db.Exec(addColumn); err != nil {
		t.Fatalf("execute generated ALTER TABLE ADD COLUMN: %v\nSQL: %s", err, addColumn)
	}
	renameColumn := buildAlterTableRenameColumnSQL(tableName, dal.FieldName(`added "column"`), dal.FieldName(`renamed "column"`))
	if _, err = db.Exec(renameColumn); err != nil {
		t.Fatalf("execute generated ALTER TABLE RENAME COLUMN: %v\nSQL: %s", err, renameColumn)
	}
	dropColumn := buildAlterTableDropColumnSQL(tableName, dal.FieldName(`renamed "column"`))
	if _, err = db.Exec(dropColumn); err != nil {
		t.Fatalf("execute generated ALTER TABLE DROP COLUMN: %v\nSQL: %s", err, dropColumn)
	}

	insert := `INSERT INTO "Product ""ProductPhoto""" ("O""Brien", "display name") VALUES (?, ?)`
	if _, err = db.Exec(insert, "value", "shown"); err != nil {
		t.Fatalf("insert into generated table: %v", err)
	}
	var got string
	if err = db.QueryRow(`SELECT "O""Brien" FROM "Product ""ProductPhoto"""`).Scan(&got); err != nil {
		t.Fatalf("read generated table: %v", err)
	}
	if got != "value" {
		t.Fatalf("queried value = %q, want value", got)
	}

	if _, err = db.Exec(buildDropIndexSQL(`idx "display"`, ddl.Options{})); err != nil {
		t.Fatalf("execute generated DROP INDEX: %v", err)
	}
	if _, err = db.Exec(buildDropTableSQL(tableName, ddl.Options{})); err != nil {
		t.Fatalf("execute generated DROP TABLE: %v", err)
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
	want := `CREATE INDEX "ix_users_email" ON "users" ("email")`
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
	want := `CREATE UNIQUE INDEX "uq_users_email" ON "users" ("email")`
	if got != want {
		t.Errorf("buildCreateIndexSQL unique mismatch.\n  got:  %s\n  want: %s", got, want)
	}
}

func TestBuildDropTableSQL(t *testing.T) {
	t.Parallel()
	got := buildDropTableSQL("users", ddl.Options{})
	want := `DROP TABLE "users"`
	if got != want {
		t.Errorf("buildDropTableSQL mismatch: got %q, want %q", got, want)
	}
	gotIf := buildDropTableSQL("users", ddl.ResolveOptions(ddl.IfExists()))
	wantIf := `DROP TABLE IF EXISTS "users"`
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
	want := `ALTER TABLE "users" ADD COLUMN "age" INTEGER`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildAlterTableDropColumn(t *testing.T) {
	t.Parallel()
	got := buildAlterTableDropColumnSQL("users", dal.FieldName("age"))
	want := `ALTER TABLE "users" DROP COLUMN "age"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildAlterTableRenameColumn(t *testing.T) {
	t.Parallel()
	got := buildAlterTableRenameColumnSQL("users", dal.FieldName("email"), dal.FieldName("email_address"))
	want := `ALTER TABLE "users" RENAME COLUMN "email" TO "email_address"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildDropIndexSQL(t *testing.T) {
	t.Parallel()
	got := buildDropIndexSQL("ix_users_email", ddl.Options{})
	want := `DROP INDEX "ix_users_email"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	gotIf := buildDropIndexSQL("ix_users_email", ddl.ResolveOptions(ddl.IfExists()))
	wantIf := `DROP INDEX IF EXISTS "ix_users_email"`
	if gotIf != wantIf {
		t.Errorf("got %q, want %q", gotIf, wantIf)
	}
}

func TestBuildCreateIndexSQL_ValidationAndOptions(t *testing.T) {
	t.Parallel()
	// Name empty
	if _, err := buildCreateIndexSQL(dbschema.IndexDef{}, ddl.Options{}); err == nil {
		t.Error("expected error for empty index name")
	}
	// Collection empty
	if _, err := buildCreateIndexSQL(dbschema.IndexDef{Name: "idx"}, ddl.Options{}); err == nil {
		t.Error("expected error for empty collection")
	}
	// Fields empty
	if _, err := buildCreateIndexSQL(dbschema.IndexDef{Name: "idx", Collection: "users"}, ddl.Options{}); err == nil {
		t.Error("expected error for empty fields")
	}
	// IfNotExists
	got, err := buildCreateIndexSQL(dbschema.IndexDef{
		Name: "ix_email", Collection: "users", Fields: []dal.FieldName{"email"},
	}, ddl.ResolveOptions(ddl.IfNotExists()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "IF NOT EXISTS") {
		t.Errorf("expected IF NOT EXISTS in %q", got)
	}
}

func TestBuildAlterTableAddColumnSQL_InvalidType(t *testing.T) {
	t.Parallel()
	_, err := buildAlterTableAddColumnSQL("users", dbschema.FieldDef{
		Name: dal.FieldName("x"), Type: dbschema.Null,
	})
	if err == nil {
		t.Fatal("expected error for invalid type in buildAlterTableAddColumnSQL")
	}
}

func TestFieldHasAutoIncIntPK_False(t *testing.T) {
	t.Parallel()
	c := dbschema.CollectionDef{
		Fields: []dbschema.FieldDef{
			{Name: dal.FieldName("name"), Type: dbschema.String},
		},
	}
	if fieldHasAutoIncIntPK(c, dal.FieldName("name")) {
		t.Error("expected false for string field")
	}
	if fieldHasAutoIncIntPK(c, dal.FieldName("missing")) {
		t.Error("expected false for missing field")
	}
}
