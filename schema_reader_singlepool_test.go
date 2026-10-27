package dalgo2sqlite

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

// singleConnPoolDeadline bounds a single-pool read only so a regression fails
// instead of hanging the package: every read below must be far quicker.
const singleConnPoolDeadline = 15 * time.Second

// openSingleConnectionMemorySQLDB opens a caller-owned in-memory SQLite pool
// that can serve exactly one connection. A schema read that holds a result set
// open while issuing another query waits for itself until the caller's context
// expires; a correct read drains and closes the rows first.
func openSingleConnectionMemorySQLDB(t *testing.T) *sql.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	sqlDB, err := sql.Open("sqlite", "file:"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return sqlDB
}

func singleConnPoolContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), singleConnPoolDeadline)
	t.Cleanup(cancel)
	return ctx
}

// seedSingleConnPool creates a schema that reaches every nested read of the
// reader: an AUTOINCREMENT probe and time-marker lookup beside the column rows,
// an index whose fields are read while listing indexes, an unnamed foreign-key
// target resolved through the parent's primary key, and a view.
func seedSingleConnPool(t *testing.T, sqlDB *sql.DB) {
	t.Helper()
	execAll(t, &Database{sqlDB: sqlDB},
		`CREATE TABLE authors (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			born_on TEXT,
			rating NUMERIC(10,2),
			UNIQUE (name))`,
		`CREATE TABLE books (
			id INTEGER PRIMARY KEY,
			author_id INTEGER NOT NULL REFERENCES authors,
			title TEXT NOT NULL,
			isbn TEXT UNIQUE,
			price REAL)`,
		`CREATE INDEX ix_books_author ON books(author_id)`,
		`CREATE VIEW recent_books AS SELECT id, title FROM books`,
		`CREATE TABLE `+timeMarkerTable+` (collection_name TEXT NOT NULL, column_name TEXT NOT NULL,
			PRIMARY KEY (collection_name, column_name))`,
		`INSERT INTO `+timeMarkerTable+` VALUES ('authors', 'born_on')`,
		`INSERT INTO authors (name, born_on) VALUES ('Ada', '2020-01-02T03:04:05Z')`,
		`INSERT INTO books (id, author_id, title, isbn) VALUES (1, 1, 'Notes', 'n-1')`,
	)
}

// checkSingleConnPoolRead fails a read that hit the context deadline instead of
// answering: on a single-connection pool that is the signature of a nested
// query waiting for the rows of the outer one to release the connection.
func checkSingleConnPoolRead(t *testing.T, op string, err error) {
	t.Helper()
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%s did not finish within %s: a schema read held the pool's only connection for a nested query", op, singleConnPoolDeadline)
	}
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
}

func collectionNames(refs []dal.CollectionRef) string {
	names := make([]string, len(refs))
	for i, ref := range refs {
		names[i] = ref.Name()
	}
	return strings.Join(names, ",")
}

func fieldTypes(def *dbschema.CollectionDef) string {
	parts := make([]string, len(def.Fields))
	for i, field := range def.Fields {
		parts[i] = string(field.Name) + ":" + field.Type.String()
	}
	return strings.Join(parts, ",")
}

func fieldNames(fields []dal.FieldName) string {
	names := make([]string, len(fields))
	for i, field := range fields {
		names[i] = string(field)
	}
	return strings.Join(names, ",")
}

func indexFields(indexes []dbschema.IndexDef) map[string]string {
	out := make(map[string]string, len(indexes))
	for _, index := range indexes {
		out[index.Name] = fieldNames(index.Fields)
	}
	return out
}

func constraintTypes(constraints []dbschema.ConstraintDef) map[string]string {
	out := make(map[string]string, len(constraints))
	for _, constraint := range constraints {
		out[constraint.Name] = constraint.Type
	}
	return out
}

// assertSingleConnPoolUsable proves the borrowed pool is still open, still
// limited to one connection, holds no connection after the reads, and still
// serves queries and writes.
func assertSingleConnPoolUsable(t *testing.T, sqlDB *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), singleConnPoolDeadline)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		t.Fatalf("the schema reader must leave the caller's pool open: %v", err)
	}
	stats := sqlDB.Stats()
	if stats.MaxOpenConnections != 1 {
		t.Fatalf("MaxOpenConnections = %d, want the caller's limit of 1", stats.MaxOpenConnections)
	}
	if stats.InUse != 0 {
		t.Fatalf("schema reads left %d pool connections in use, want none", stats.InUse)
	}
	if _, err := sqlDB.ExecContext(ctx, `INSERT INTO books (id, author_id, title) VALUES (2, 1, 'More')`); err != nil {
		t.Fatalf("writing through the borrowed pool after schema reads: %v", err)
	}
	var books int
	if err := sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM books`).Scan(&books); err != nil {
		t.Fatalf("reading through the borrowed pool after schema reads: %v", err)
	}
	if books != 2 {
		t.Fatalf("books = %d, want the row written after the schema reads", books)
	}
}

func TestSingleConnectionPool_StrictSchemaReader(t *testing.T) {
	sqlDB := openSingleConnectionMemorySQLDB(t)
	seedSingleConnPool(t, sqlDB)
	ctx := singleConnPoolContext(t)

	reader, err := NewSchemaReader(sqlDB)
	if err != nil {
		t.Fatalf("NewSchemaReader: %v", err)
	}

	collections, err := reader.ListCollections(ctx, nil)
	checkSingleConnPoolRead(t, "ListCollections", err)
	if got := collectionNames(collections); got != "authors,books" {
		t.Fatalf("ListCollections = %s, want authors,books (no view, no marker table)", got)
	}

	// The AUTOINCREMENT probe and the time-marker lookup are nested reads
	// beside pragma_table_info.
	authors, err := reader.DescribeCollection(ctx, rootRef("authors"))
	checkSingleConnPoolRead(t, "DescribeCollection(authors)", err)
	if got := fieldTypes(authors); got != "id:int,name:string,born_on:time,rating:decimal" {
		t.Fatalf("authors fields = %s, want id:int,name:string,born_on:time,rating:decimal", got)
	}
	if !authors.Fields[0].AutoIncrement {
		t.Error("authors.id must be detected as AUTOINCREMENT")
	}
	if authors.Fields[0].Nullable || authors.Fields[1].Nullable {
		t.Errorf("authors.id and authors.name must be non-nullable: %+v", authors.Fields)
	}
	if !authors.Fields[2].Nullable || !authors.Fields[3].Nullable {
		t.Errorf("authors.born_on and authors.rating must stay nullable: %+v", authors.Fields)
	}
	if authors.Fields[3].Precision == nil || authors.Fields[3].Precision.Total != 10 || authors.Fields[3].Precision.Scale != 2 {
		t.Errorf("authors.rating precision = %+v, want NUMERIC(10,2)", authors.Fields[3].Precision)
	}
	if fieldNames(authors.PrimaryKey) != "id" {
		t.Errorf("authors primary key = %s, want id", fieldNames(authors.PrimaryKey))
	}
	if got := indexFields(authors.Indexes); !reflect.DeepEqual(got, map[string]string{"sqlite_autoindex_authors_1": "name"}) {
		t.Errorf("authors indexes = %v, want only the UNIQUE(name) index", got)
	}
	if !strings.Contains(authors.SourceDefinition.CreateSQL, "AUTOINCREMENT") {
		t.Errorf("authors source DDL = %q, want the AUTOINCREMENT table", authors.SourceDefinition.CreateSQL)
	}
	if len(authors.ForeignKeys) != 0 {
		t.Errorf("authors foreign keys = %+v, want none", authors.ForeignKeys)
	}

	// Reading each index's fields while listing the index list is another
	// nested read; the foreign key names no target column, so its target
	// primary key is read through a nested query too.
	books, err := reader.DescribeCollection(ctx, rootRef("books"))
	checkSingleConnPoolRead(t, "DescribeCollection(books)", err)
	if got := fieldTypes(books); got != "id:int,author_id:int,title:string,isbn:string,price:float" {
		t.Fatalf("books fields = %s, want id:int,author_id:int,title:string,isbn:string,price:float", got)
	}
	if books.Fields[0].AutoIncrement {
		t.Error("books.id is a plain INTEGER PRIMARY KEY, not AUTOINCREMENT")
	}
	if got := indexFields(books.Indexes); !reflect.DeepEqual(got, map[string]string{
		"ix_books_author":          "author_id",
		"sqlite_autoindex_books_1": "isbn",
	}) {
		t.Errorf("books indexes = %v, want ix_books_author and the UNIQUE(isbn) index", got)
	}
	if len(books.ForeignKeys) != 1 {
		t.Fatalf("books foreign keys = %+v, want one", books.ForeignKeys)
	}
	key := books.ForeignKeys[0]
	if key.ReferencedCollection != "authors" || fieldNames(key.Fields) != "author_id" || fieldNames(key.ReferencedFields) != "id" {
		t.Errorf("books foreign key = %+v, want author_id -> authors.id from the parent primary key", key)
	}

	indexes, err := reader.ListIndexes(ctx, rootRef("books"))
	checkSingleConnPoolRead(t, "ListIndexes(books)", err)
	if got := indexFields(indexes); !reflect.DeepEqual(got, map[string]string{
		"ix_books_author":          "author_id",
		"sqlite_autoindex_books_1": "isbn",
	}) {
		t.Errorf("ListIndexes(books) = %v, want both indexes with their fields", got)
	}

	constraints, err := reader.ListConstraints(ctx, rootRef("books"))
	checkSingleConnPoolRead(t, "ListConstraints(books)", err)
	if got := constraintTypes(constraints); !reflect.DeepEqual(got, map[string]string{
		"books_pk":                 "primary-key",
		"books_fk_0":               "foreign-key",
		"sqlite_autoindex_books_1": "unique",
	}) {
		t.Errorf("ListConstraints(books) = %v, want pk, fk and the UNIQUE constraint", got)
	}

	referrers, err := reader.ListReferrers(ctx, rootRef("authors"))
	checkSingleConnPoolRead(t, "ListReferrers(authors)", err)
	if len(referrers) != 1 || referrers[0].Collection.Name() != "books" || fieldNames(referrers[0].Fields) != "author_id" {
		t.Errorf("ListReferrers(authors) = %+v, want books.author_id", referrers)
	}
	none, err := reader.ListReferrers(ctx, rootRef("books"))
	checkSingleConnPoolRead(t, "ListReferrers(books)", err)
	if len(none) != 0 {
		t.Errorf("ListReferrers(books) = %+v, want none", none)
	}

	if err := ctx.Err(); err != nil {
		t.Fatalf("the reads did not finish before the caller's deadline: %v", err)
	}
	assertSingleConnPoolUsable(t, sqlDB)
}

func TestSingleConnectionPool_LenientSchemaReader(t *testing.T) {
	sqlDB := openSingleConnectionMemorySQLDB(t)
	seedSingleConnPool(t, sqlDB)
	ctx := singleConnPoolContext(t)

	lenient, err := NewLenientSchemaReader(sqlDB)
	if err != nil {
		t.Fatalf("NewLenientSchemaReader: %v", err)
	}
	strict, err := NewSchemaReader(sqlDB)
	if err != nil {
		t.Fatalf("NewSchemaReader: %v", err)
	}

	// Describing a view reads its source definition and columns through
	// nested queries beside the view listing.
	view, err := lenient.DescribeCollection(ctx, rootRef("recent_books"))
	checkSingleConnPoolRead(t, "lenient DescribeCollection(view)", err)
	if got := fieldTypes(view); got != "id:int,title:string" {
		t.Fatalf("view fields = %s, want id:int,title:string", got)
	}
	if len(view.PrimaryKey) != 0 {
		t.Errorf("view primary key = %s, want none", fieldNames(view.PrimaryKey))
	}
	if len(view.Indexes) != 0 || len(view.ForeignKeys) != 0 {
		t.Errorf("view indexes/foreign keys = %+v/%+v, want none", view.Indexes, view.ForeignKeys)
	}
	if !strings.Contains(view.SourceDefinition.CreateSQL, "CREATE VIEW") {
		t.Errorf("view source DDL = %q, want the CREATE VIEW statement", view.SourceDefinition.CreateSQL)
	}
	indexes, err := lenient.ListIndexes(ctx, rootRef("recent_books"))
	checkSingleConnPoolRead(t, "lenient ListIndexes(view)", err)
	if len(indexes) != 0 {
		t.Errorf("lenient ListIndexes(view) = %+v, want none", indexes)
	}
	constraints, err := lenient.ListConstraints(ctx, rootRef("recent_books"))
	checkSingleConnPoolRead(t, "lenient ListConstraints(view)", err)
	if len(constraints) != 0 {
		t.Errorf("lenient ListConstraints(view) = %+v, want none", constraints)
	}
	referrers, err := lenient.ListReferrers(ctx, rootRef("recent_books"))
	checkSingleConnPoolRead(t, "lenient ListReferrers(view)", err)
	if len(referrers) != 0 {
		t.Errorf("lenient ListReferrers(view) = %+v, want none", referrers)
	}

	// The lenient strict/lenient switch is the only difference: an AUTOINCREMENT
	// table is described the same way, still without a second pool.
	authors, err := lenient.DescribeCollection(ctx, rootRef("authors"))
	checkSingleConnPoolRead(t, "lenient DescribeCollection(authors)", err)
	if got := fieldTypes(authors); got != "id:int,name:string,born_on:time,rating:decimal" {
		t.Fatalf("lenient authors fields = %s, want id:int,name:string,born_on:time,rating:decimal", got)
	}
	if !authors.Fields[0].AutoIncrement {
		t.Error("lenient authors.id must be detected as AUTOINCREMENT")
	}

	if _, err := strict.DescribeCollection(ctx, rootRef("recent_books")); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("strict DescribeCollection(view) = %v, want not found", err)
	}

	if err := ctx.Err(); err != nil {
		t.Fatalf("the reads did not finish before the caller's deadline: %v", err)
	}
	assertSingleConnPoolUsable(t, sqlDB)
}

func TestSingleConnectionPool_LegacyDatabaseReader(t *testing.T) {
	sqlDB := openSingleConnectionMemorySQLDB(t)
	seedSingleConnPool(t, sqlDB)
	ctx := singleConnPoolContext(t)

	strict, err := NewSchemaReader(sqlDB)
	if err != nil {
		t.Fatalf("NewSchemaReader: %v", err)
	}
	lenient, err := NewLenientSchemaReader(sqlDB)
	if err != nil {
		t.Fatalf("NewLenientSchemaReader: %v", err)
	}
	legacy := &Database{sqlDB: sqlDB}

	// The helper fix is shared: the existing Database-backed reader must not
	// deadlock its own single-connection pool either, and must return the same
	// metadata as the narrowed reader.
	assertSameSchemaReader(t, ctx, strict, legacy, []*dal.CollectionRef{rootRef("authors"), rootRef("books")})
	assertSameSchemaReader(t, ctx, lenient, legacy.LenientSchema(), []*dal.CollectionRef{
		rootRef("authors"), rootRef("books"), rootRef("recent_books"),
	})

	// Listing source views reads the columns of each view while the view
	// listing rows used to stay open.
	views, err := legacy.ListSourceViews(ctx)
	checkSingleConnPoolRead(t, "legacy ListSourceViews", err)
	if len(views) != 1 || views[0].Name != "recent_books" || strings.Join(views[0].Columns, ",") != "id,title" {
		t.Fatalf("ListSourceViews = %+v, want recent_books(id,title)", views)
	}

	if err := ctx.Err(); err != nil {
		t.Fatalf("the reads did not finish before the caller's deadline: %v", err)
	}
	assertSingleConnPoolUsable(t, sqlDB)
}
