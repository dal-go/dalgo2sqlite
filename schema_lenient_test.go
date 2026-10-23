package dalgo2sqlite

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

func execAll(t *testing.T, db *Database, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.sqlDB.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func TestLenientSchema_SharesTheConnectionAndLeavesTheOriginalStrict(t *testing.T) {
	db := openTestDB(t)
	lenient := db.LenientSchema()
	if lenient == db || !lenient.lenient || db.lenient || lenient.sqlDB != db.sqlDB {
		t.Fatalf("LenientSchema() = %+v from %+v", lenient, db)
	}
}

func TestLenientSchema_DescribesUnmappedDeclaredTypesAsNull(t *testing.T) {
	db := openTestDB(t)
	execAll(t, db, `CREATE TABLE things (id INTEGER PRIMARY KEY, doc JSON, untyped, code UUID NOT NULL DEFAULT 'x')`)
	ref := dal.NewRootCollectionRef("things", "")
	if _, err := db.DescribeCollection(context.Background(), &ref); err == nil {
		t.Fatal("the strict reader must still refuse an unrecognized declared type")
	}
	def, err := db.LenientSchema().DescribeCollection(context.Background(), &ref)
	if err != nil {
		t.Fatalf("DescribeCollection: %v", err)
	}
	want := []dbschema.Type{dbschema.Int, dbschema.Null, dbschema.Null, dbschema.Null}
	for i, field := range def.Fields {
		if field.Type != want[i] {
			t.Errorf("field %s type = %v, want %v", field.Name, field.Type, want[i])
		}
	}
	declared := []string{"INTEGER", "JSON", "", "UUID"}
	for i, column := range def.SourceDefinition.Columns {
		if column.DeclaredType != declared[i] {
			t.Errorf("column %s declared type = %q, want %q", column.Name, column.DeclaredType, declared[i])
		}
	}
	if def.Fields[3].Nullable || def.SourceDefinition.Columns[3].DefaultSQL == nil || *def.SourceDefinition.Columns[3].DefaultSQL != "'x'" {
		t.Errorf("code column: %+v %+v", def.Fields[3], def.SourceDefinition.Columns[3])
	}
}

func TestLenientSchema_LeavesGeneratedAndHiddenColumnsOut(t *testing.T) {
	db := openTestDB(t)
	execAll(t, db,
		`CREATE TABLE generated (id INTEGER PRIMARY KEY, a INT, b INT GENERATED ALWAYS AS (a * 2) VIRTUAL, c INT GENERATED ALWAYS AS (a * 3) STORED)`,
		`CREATE VIRTUAL TABLE docs USING fts5(title, body)`,
	)
	ctx := context.Background()
	lenient := db.LenientSchema()
	for name, want := range map[string][]string{"generated": {"id", "a"}, "docs": {"title", "body"}} {
		ref := dal.NewRootCollectionRef(name, "")
		def, err := lenient.DescribeCollection(ctx, &ref)
		if err != nil {
			t.Fatalf("DescribeCollection(%s): %v", name, err)
		}
		var got []string
		for _, field := range def.Fields {
			got = append(got, string(field.Name))
		}
		if strings.Join(got, ",") != strings.Join(want, ",") || len(def.SourceDefinition.Columns) != len(want) {
			t.Errorf("%s fields = %v, source columns %+v, want %v", name, got, def.SourceDefinition.Columns, want)
		}
		if _, err := db.DescribeCollection(ctx, &ref); err == nil {
			t.Errorf("the strict reader must still refuse %s", name)
		}
		if _, err := lenient.OpenSourceRows(ctx, &ref); err == nil || !strings.Contains(err.Error(), "unsupported for lossless export") {
			t.Errorf("rows of %s must still be refused for export: %v", name, err)
		}
	}
}

func TestLenientSchema_DescribesViews(t *testing.T) {
	db := openTestDB(t)
	execAll(t, db,
		`CREATE TABLE customer (id INTEGER PRIMARY KEY, first TEXT NOT NULL, last TEXT)`,
		`CREATE VIEW names AS SELECT id, first || ' ' || last AS full_name FROM customer`,
	)
	ctx := context.Background()
	ref := dal.NewRootCollectionRef("names", "")
	if _, err := db.DescribeCollection(ctx, &ref); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("the strict reader must not describe a view: %v", err)
	}
	def, err := db.LenientSchema().DescribeCollection(ctx, &ref)
	if err != nil {
		t.Fatalf("DescribeCollection: %v", err)
	}
	if len(def.Fields) != 2 || def.Fields[0].Name != "id" || def.Fields[0].Type != dbschema.Int ||
		def.Fields[1].Name != "full_name" || def.Fields[1].Type != dbschema.Null || !def.Fields[1].Nullable {
		t.Fatalf("view fields: %+v", def.Fields)
	}
	if len(def.PrimaryKey) != 0 || len(def.Indexes) != 0 || len(def.ForeignKeys) != 0 {
		t.Fatalf("a view has no keys or indexes: %+v", def)
	}
	if !strings.HasPrefix(def.SourceDefinition.CreateSQL, "CREATE VIEW names") || def.SourceDefinition.Columns[0].DeclaredType != "INTEGER" {
		t.Fatalf("view source definition: %+v", def.SourceDefinition)
	}
}

func TestLenientSchema_ViewThatCannotBeReadFails(t *testing.T) {
	db := openTestDB(t)
	execAll(t, db,
		`CREATE TABLE doomed (id INTEGER PRIMARY KEY)`,
		`CREATE VIEW broken AS SELECT id FROM doomed`,
		`DROP TABLE doomed`,
	)
	ref := dal.NewRootCollectionRef("broken", "")
	if _, err := db.LenientSchema().DescribeCollection(context.Background(), &ref); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("a view whose definition does not work must fail its description: %v", err)
	}
}

func TestListViews(t *testing.T) {
	db := openTestDB(t)
	execAll(t, db,
		`CREATE TABLE t (id INTEGER PRIMARY KEY)`,
		`CREATE VIEW zeta AS SELECT id FROM t`,
		`CREATE VIEW alpha AS SELECT id FROM t`,
	)
	views, err := db.ListViews(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || views[0].Name() != "alpha" || views[1].Name() != "zeta" {
		t.Fatalf("ListViews = %v", views)
	}
	tables, err := db.ListCollections(context.Background(), nil)
	if err != nil || len(tables) != 1 || tables[0].Name() != "t" {
		t.Fatalf("ListCollections = %v, %v: a view is not a collection", tables, err)
	}
}

func TestListViews_Faults(t *testing.T) {
	for _, mode := range []string{"query", "scan"} {
		t.Run(mode, func(t *testing.T) {
			db, hooks := sourceHookDB(t)
			failQueryAt(hooks, `WHERE type='view' ORDER BY name`, mode)
			if _, err := db.ListViews(context.Background()); err == nil {
				t.Fatal("a fault listing the views was ignored")
			}
		})
	}
}

func TestListCollections_KeepsTablesThatOnlyLookLikeSQLitesOwn(t *testing.T) {
	db := openTestDB(t)
	execAll(t, db,
		`CREATE TABLE sqliteish (id INT)`,
		`CREATE TABLE counter (id INTEGER PRIMARY KEY AUTOINCREMENT)`,
		`INSERT INTO counter DEFAULT VALUES`,
	)
	tables, err := db.ListCollections(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, table := range tables {
		names = append(names, table.Name())
	}
	if strings.Join(names, ",") != "counter,sqliteish" {
		t.Fatalf("ListCollections = %v, want counter and sqliteish, not sqlite_sequence", names)
	}
}

func TestListConstraints_Unique(t *testing.T) {
	db := openTestDB(t)
	execAll(t, db,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, a TEXT UNIQUE, b INT, c INT, UNIQUE (b, c))`,
		`CREATE UNIQUE INDEX t_b ON t(b)`,
	)
	ctx := context.Background()
	ref := dal.NewRootCollectionRef("t", "")
	constraints, err := db.ListConstraints(ctx, &ref)
	if err != nil {
		t.Fatal(err)
	}
	var unique []string
	for _, constraint := range constraints {
		if constraint.Type == "unique" {
			unique = append(unique, constraint.Name)
		}
	}
	// A unique index made by CREATE UNIQUE INDEX is an index, not a constraint.
	sort.Strings(unique)
	if strings.Join(unique, ",") != "sqlite_autoindex_t_1,sqlite_autoindex_t_2" {
		t.Fatalf("unique constraints = %v (all: %+v)", unique, constraints)
	}
	indexes, err := db.ListIndexes(ctx, &ref)
	if err != nil {
		t.Fatal(err)
	}
	columns := map[string]string{}
	for _, index := range indexes {
		var names []string
		for _, field := range index.Fields {
			names = append(names, string(field))
		}
		columns[index.Name] = strings.Join(names, ",")
	}
	if columns["sqlite_autoindex_t_1"] != "a" || columns["sqlite_autoindex_t_2"] != "b,c" {
		t.Fatalf("the index of each unique constraint names its columns: %v", columns)
	}
}

func TestListConstraints_UniqueFaults(t *testing.T) {
	for _, mode := range []string{"query", "scan", "rows"} {
		t.Run(mode, func(t *testing.T) {
			db, hooks := sourceHookDB(t)
			failQueryAt(hooks, `WHERE origin='u'`, mode)
			ref := dal.NewRootCollectionRef("source", "")
			if _, err := db.ListConstraints(context.Background(), &ref); err == nil {
				t.Fatal("a fault listing the unique constraints was ignored")
			}
		})
	}
}

func TestListConstraints_ForeignKeyRowsFault(t *testing.T) {
	db, hooks := sourceHookDB(t)
	failQueryAt(hooks, `SELECT DISTINCT id FROM pragma_foreign_key_list`, "rows")
	ref := dal.NewRootCollectionRef("source", "")
	if _, err := db.ListConstraints(context.Background(), &ref); err == nil {
		t.Fatal("a fault reading the foreign keys was ignored")
	}
}

func TestForeignKeys_NameTheTableAsTheDatabaseSpellsIt(t *testing.T) {
	db := openTestDB(t)
	execAll(t, db,
		`CREATE TABLE Parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES PARENT, gone_id INTEGER REFERENCES Gone(id))`,
	)
	ctx := context.Background()
	ref := dal.NewRootCollectionRef("child", "")
	def, err := db.DescribeCollection(ctx, &ref)
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]string{}
	for _, key := range def.ForeignKeys {
		var fields []string
		for _, field := range key.ReferencedFields {
			fields = append(fields, string(field))
		}
		targets[string(key.Fields[0])] = key.ReferencedCollection + "(" + strings.Join(fields, ",") + ")"
	}
	if targets["parent_id"] != "Parent(id)" || targets["gone_id"] != "Gone(id)" {
		t.Fatalf("foreign key targets = %v", targets)
	}
	parent := dal.NewRootCollectionRef("Parent", "")
	referrers, err := db.ListReferrers(ctx, &parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(referrers) != 1 || referrers[0].Collection.Name() != "child" || len(referrers[0].Fields) != 1 || referrers[0].Fields[0] != "parent_id" {
		t.Fatalf("a key that names the table in another letter case refers to it: %+v", referrers)
	}
}

func TestForeignKeys_TargetLookupFault(t *testing.T) {
	db, hooks := sourceHookDB(t)
	if _, err := db.sqlDB.ExecContext(context.Background(), `CREATE TABLE child (id INTEGER PRIMARY KEY, source_id INTEGER REFERENCES source(id))`); err != nil {
		t.Fatal(err)
	}
	failQueryAt(hooks, `name=? COLLATE NOCASE`, "query")
	if _, err := readForeignKeys(context.Background(), db.sqlDB, "child"); err == nil {
		t.Fatal("a fault looking up the table a key refers to was ignored")
	}
}

func TestUnnamedTablesAndViews(t *testing.T) {
	// SQLite keeps tables and views in one namespace: one file can hold an
	// unnamed table or an unnamed view, not both.
	tables, views := openTestDB(t), openTestDB(t)
	execAll(t, tables, `CREATE TABLE "" (a INTEGER)`, `CREATE TABLE kept (id INTEGER PRIMARY KEY)`)
	execAll(t, views, `CREATE VIEW "" AS SELECT 1 AS x`, `CREATE VIEW seen AS SELECT 1 AS y`)
	ctx := context.Background()
	names := func(refs []dal.CollectionRef, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, ref := range refs {
			out = append(out, "["+ref.Name()+"]")
		}
		return strings.Join(out, ",")
	}
	if got := names(tables.ListCollections(ctx, nil)); got != "[kept]" {
		t.Errorf("strict ListCollections = %s, want the unnamed table left out", got)
	}
	if got := names(views.ListViews(ctx)); got != "[seen]" {
		t.Errorf("strict ListViews = %s, want the unnamed view left out", got)
	}
	if got := names(tables.LenientSchema().ListCollections(ctx, nil)); got != "[],[kept]" {
		t.Errorf("lenient ListCollections = %s, want the unnamed table as a zero reference", got)
	}
	if got := names(views.LenientSchema().ListViews(ctx)); got != "[],[seen]" {
		t.Errorf("lenient ListViews = %s, want the unnamed view as a zero reference", got)
	}
	ref := dal.NewRootCollectionRef("kept", "")
	if referrers, err := tables.LenientSchema().ListReferrers(ctx, &ref); err != nil || len(referrers) != 0 {
		t.Errorf("ListReferrers = %+v, %v: an unnamed table refers to nothing", referrers, err)
	}
}

func TestDescribeCollection_WithoutRowIDPrimaryKeyIndexHasNoDDL(t *testing.T) {
	db := openTestDB(t)
	execAll(t, db, `CREATE TABLE wr (k TEXT PRIMARY KEY, v TEXT) WITHOUT ROWID`)
	ref := dal.NewRootCollectionRef("wr", "")
	def, err := db.DescribeCollection(context.Background(), &ref)
	if err != nil {
		t.Fatalf("DescribeCollection: %v", err)
	}
	if len(def.SourceDefinition.Indexes) != 1 || def.SourceDefinition.Indexes[0].Origin != "pk" || def.SourceDefinition.Indexes[0].CreateSQL != "" {
		t.Fatalf("source indexes = %+v", def.SourceDefinition.Indexes)
	}
	if def.Fields[0].Nullable {
		t.Errorf("the primary key of a WITHOUT ROWID table cannot be NULL: %+v", def.Fields[0])
	}
}
