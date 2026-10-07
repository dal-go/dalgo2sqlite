package dalgo2sqlite

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

var _ dbschema.SourceViewReader = (*Database)(nil)

func TestSourceDefinitionPreservesSQLiteOnlySchemaDetails(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	parentDDL := `CREATE TABLE parent (
		code TEXT COLLATE NOCASE PRIMARY KEY,
		amount DECIMAL(10,2) DEFAULT (1.25) CHECK (amount >= 0)
	)`
	childDDL := `CREATE TABLE child (
		a TEXT, b TEXT DEFAULT NULL,
		FOREIGN KEY (a) REFERENCES parent(code) ON UPDATE CASCADE ON DELETE SET NULL
	)`
	indexDDL := `CREATE UNIQUE INDEX child_expr_partial ON child(lower(a) COLLATE NOCASE DESC) WHERE b IS NOT NULL`
	viewDDL := `CREATE VIEW child_view AS SELECT a, b FROM child`
	for _, statement := range []string{parentDDL, childDDL, indexDDL, viewDDL} {
		if _, err := db.sqlDB.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	parentRef := dal.NewRootCollectionRef("parent", "")
	parent, err := db.DescribeCollection(ctx, &parentRef)
	if err != nil {
		t.Fatal(err)
	}
	if parent.SourceDefinition == nil || parent.SourceDefinition.Dialect != "sqlite" || parent.SourceDefinition.CreateSQL != parentDDL {
		t.Fatalf("source parent definition: %+v", parent.SourceDefinition)
	}
	if !parent.Fields[0].Nullable || parent.SourceDefinition.Columns[0].NotNull || parent.SourceDefinition.Columns[0].PrimaryKeyPosition != 1 {
		t.Fatalf("SQLite TEXT PK raw/effective nullability: field=%+v source=%+v", parent.Fields[0], parent.SourceDefinition.Columns[0])
	}
	if parent.SourceDefinition.Columns[1].DeclaredType != "DECIMAL(10,2)" ||
		parent.SourceDefinition.Columns[1].DefaultSQL == nil || *parent.SourceDefinition.Columns[1].DefaultSQL != "1.25" {
		t.Fatalf("declared decimal/default: %+v", parent.SourceDefinition.Columns[1])
	}
	if len(parent.SourceDefinition.Indexes) != 1 || parent.SourceDefinition.Indexes[0].Origin != "pk" || !parent.SourceDefinition.Indexes[0].Unique {
		t.Fatalf("PK autoindex: %+v", parent.SourceDefinition.Indexes)
	}
	childRef := dal.NewRootCollectionRef("child", "")
	child, err := db.DescribeCollection(ctx, &childRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(child.PrimaryKey) != 0 || child.SourceDefinition.CreateSQL != childDDL || len(child.ForeignKeys) != 1 ||
		child.ForeignKeys[0].OnUpdate != "CASCADE" || child.ForeignKeys[0].OnDelete != "SET NULL" {
		t.Fatalf("keyless child/FK: %+v", child)
	}
	if child.SourceDefinition.Columns[1].DefaultSQL == nil || *child.SourceDefinition.Columns[1].DefaultSQL != "NULL" {
		t.Fatalf("explicit DEFAULT NULL lost: %+v", child.SourceDefinition.Columns[1])
	}
	if len(child.SourceDefinition.Indexes) != 1 {
		t.Fatalf("child indexes: %+v", child.SourceDefinition.Indexes)
	}
	if len(child.Indexes) != 0 {
		t.Fatalf("expression-only index must not become invalid portable IndexDef: %+v", child.Indexes)
	}
	index := child.SourceDefinition.Indexes[0]
	if !index.Unique || !index.Partial || index.CreateSQL != indexDDL || len(index.Columns) < 1 ||
		index.Columns[0].Name != nil || !index.Columns[0].Key || !index.Columns[0].Descending || index.Columns[0].Collation != "NOCASE" {
		t.Fatalf("expression/partial index: %+v", index)
	}
	views, err := db.ListSourceViews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Name != "child_view" || views[0].CreateSQL != viewDDL || strings.Join(views[0].Columns, ",") != "a,b" {
		t.Fatalf("source views: %+v", views)
	}
}

func TestSourceRowsStreamKeepsStorageClassAndDecimalValue(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, statement := range []string{
		`CREATE TABLE amounts (a INTEGER, b INTEGER, amount NUMERIC(10,2), PRIMARY KEY (a,b))`,
		`INSERT INTO amounts VALUES (2, 1, 1.25), (1, 2, 2), (1, 1, 'text'), (3, 1, X'00FF'), (4, 1, 9007199254740993)`,
		`CREATE TABLE keyless (name TEXT)`,
		`INSERT INTO keyless VALUES ('second'), ('first')`,
		`CREATE TABLE shadowed_rowid (rowid TEXT, name TEXT)`,
		`INSERT INTO shadowed_rowid VALUES ('z', 'first'), ('a', 'second')`,
		`CREATE TABLE dates (at DATETIME NOT NULL)`,
		`INSERT INTO dates VALUES ('2021-01-01 00:00:00.000')`,
		`CREATE TABLE typed_literals (b BOOLEAN, d DATE, tm TIMESTAMP, raw BLOB, missing TEXT)`,
		`INSERT INTO typed_literals VALUES (1, '2020-02-03', '2020-02-03 04:05:06', X'00FF', NULL)`,
	} {
		if _, err := db.sqlDB.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	ref := dal.NewRootCollectionRef("amounts", "")
	cursor, err := db.OpenSourceRows(ctx, &ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cursor.Close() }()
	checks := []struct {
		a, b  int64
		value any
		class string
	}{
		{1, 1, "text", "text"},
		{1, 2, "2", "integer"},
		{2, 1, "1.25", "real"},
		{3, 1, []byte{0, 255}, "blob"},
		{4, 1, "9007199254740993", "integer"},
	}
	for _, check := range checks {
		row, err := cursor.Next()
		if err != nil {
			t.Fatal(err)
		}
		if row.Values["a"] != check.a || row.Values["b"] != check.b || row.StorageClasses["amount"] != check.class {
			t.Fatalf("ordered row/storage class: %+v", row)
		}
		switch want := check.value.(type) {
		case string:
			if row.Values["amount"] != want {
				t.Fatalf("amount=%v want %q", row.Values["amount"], want)
			}
		case []byte:
			actual, ok := row.Values["amount"].([]byte)
			if !ok || string(actual) != string(want) {
				t.Fatalf("binary amount=%v", row.Values["amount"])
			}
		}
	}
	if _, err := cursor.Next(); err != io.EOF {
		t.Fatalf("end of source rows: %v", err)
	}
	keyless := dal.NewRootCollectionRef("keyless", "")
	keylessCursor, err := db.OpenSourceRows(ctx, &keyless)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = keylessCursor.Close() }()
	for _, expected := range []string{"second", "first"} {
		row, err := keylessCursor.Next()
		if err != nil || row.Values["name"] != expected {
			t.Fatalf("keyless row=%+v err=%v want %q", row, err, expected)
		}
	}
	shadowed := dal.NewRootCollectionRef("shadowed_rowid", "")
	shadowedCursor, err := db.OpenSourceRows(ctx, &shadowed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shadowedCursor.Close() }()
	for _, expected := range []string{"first", "second"} {
		row, err := shadowedCursor.Next()
		if err != nil || row.Values["name"] != expected {
			t.Fatalf("hidden rowid order=%+v err=%v want %q", row, err, expected)
		}
	}
	dates := dal.NewRootCollectionRef("dates", "")
	dateCursor, err := db.OpenSourceRows(ctx, &dates)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dateCursor.Close() }()
	dateRow, err := dateCursor.Next()
	if err != nil || dateRow.Values["at"] != "2021-01-01 00:00:00.000" || dateRow.StorageClasses["at"] != "text" {
		t.Fatalf("DATETIME TEXT must retain lexical source value: row=%+v err=%v", dateRow, err)
	}
	literals := dal.NewRootCollectionRef("typed_literals", "")
	literalCursor, err := db.OpenSourceRows(ctx, &literals)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = literalCursor.Close() }()
	literalRow, err := literalCursor.Next()
	if err != nil || literalRow.Values["b"] != true || literalRow.StorageClasses["b"] != "integer" ||
		literalRow.Values["d"] != "2020-02-03" || literalRow.Values["tm"] != "2020-02-03 04:05:06" ||
		literalRow.StorageClasses["d"] != "text" || literalRow.StorageClasses["tm"] != "text" ||
		literalRow.Values["missing"] != nil || literalRow.StorageClasses["missing"] != "null" {
		t.Fatalf("declared-type coercions must not rewrite storage values: row=%+v err=%v", literalRow, err)
	}
	raw, ok := literalRow.Values["raw"].([]byte)
	if !ok || string(raw) != string([]byte{0, 255}) || literalRow.StorageClasses["raw"] != "blob" {
		t.Fatalf("BLOB must remain bytes: row=%+v", literalRow)
	}
}

func TestIntegerPrimaryKeyDescDoesNotClaimRowidAlias(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if _, err := db.sqlDB.ExecContext(ctx, `CREATE TABLE desc_pk (id INTEGER PRIMARY KEY DESC, label TEXT)`); err != nil {
		t.Fatal(err)
	}
	ref := dal.NewRootCollectionRef("desc_pk", "")
	definition, err := db.DescribeCollection(ctx, &ref)
	if err != nil {
		t.Fatal(err)
	}
	if !definition.Fields[0].Nullable || len(definition.SourceDefinition.Indexes) != 1 || definition.SourceDefinition.Indexes[0].Origin != "pk" {
		t.Fatalf("INTEGER PRIMARY KEY DESC is not a rowid alias: %+v", definition)
	}
}

func TestSourceConstraintCheckerUsesSQLiteSemantics(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, statement := range []string{
		`CREATE TABLE parent (code TEXT COLLATE NOCASE PRIMARY KEY)`,
		`CREATE TABLE child (code TEXT REFERENCES parent(code), amount INTEGER CHECK (amount > 0))`,
		`INSERT INTO parent VALUES ('A')`,
		`INSERT INTO child VALUES ('a', 1)`,
	} {
		if _, err := db.sqlDB.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CheckSourceConstraints(ctx); err != nil {
		t.Fatalf("NOCASE FK must be valid under SQLite semantics: %v", err)
	}
	if _, err := db.sqlDB.ExecContext(ctx, `INSERT INTO child VALUES ('missing', 2)`); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckSourceConstraints(ctx); err == nil || !strings.Contains(err.Error(), "foreign-key violation") {
		t.Fatalf("orphan must be reported: %v", err)
	}
	if _, err := db.sqlDB.ExecContext(ctx, `DELETE FROM child WHERE code='missing'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sqlDB.ExecContext(ctx, `PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sqlDB.ExecContext(ctx, `INSERT INTO child VALUES ('a', -1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckSourceConstraints(ctx); err == nil || !strings.Contains(err.Error(), "integrity violation") {
		t.Fatalf("CHECK violation must be reported: %v", err)
	}
}

func TestGeneratedColumnsFailClosedUntilRepresentable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if _, err := db.sqlDB.ExecContext(ctx, `CREATE TABLE generated (id INTEGER, doubled INTEGER GENERATED ALWAYS AS (id * 2) STORED)`); err != nil {
		t.Fatal(err)
	}
	ref := dal.NewRootCollectionRef("generated", "")
	if _, err := db.DescribeCollection(ctx, &ref); err == nil || !strings.Contains(err.Error(), "unsupported for lossless export") {
		t.Fatalf("DescribeCollection must reject omitted generated column: %v", err)
	}
	if _, err := db.OpenSourceRows(ctx, &ref); err == nil || !strings.Contains(err.Error(), "unsupported for lossless export") {
		t.Fatalf("OpenSourceRows must reject omitted generated column: %v", err)
	}
}
