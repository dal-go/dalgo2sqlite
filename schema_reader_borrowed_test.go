package dalgo2sqlite

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
)

// openSharedMemorySQLDB opens a caller-owned in-memory SQLite pool. The shared
// cache matters here: introspection reads rows while it may need another
// connection, and every connection of the pool must see the same database.
func openSharedMemorySQLDB(t *testing.T) *sql.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	sqlDB, err := sql.Open("sqlite", "file:"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return sqlDB
}

func rootRef(name string) *dal.CollectionRef {
	ref := dal.NewRootCollectionRef(name, "")
	return &ref
}

// assertSameSchemaReader runs every SchemaReader method on both readers over
// the same references and fails on any difference in metadata or fault.
func assertSameSchemaReader(t *testing.T, ctx context.Context, got, want dbschema.SchemaReader, refs []*dal.CollectionRef) {
	t.Helper()
	gotCollections, gotErr := got.ListCollections(ctx, nil)
	wantCollections, wantErr := want.ListCollections(ctx, nil)
	if gotErr != nil || wantErr != nil {
		t.Fatalf("ListCollections: narrow reader = %v, existing reader = %v", gotErr, wantErr)
	}
	if !reflect.DeepEqual(gotCollections, wantCollections) {
		t.Fatalf("ListCollections: narrow reader = %v, existing reader = %v", gotCollections, wantCollections)
	}
	for _, ref := range refs {
		gotDef, err := got.DescribeCollection(ctx, ref)
		if err != nil {
			t.Fatalf("narrow reader DescribeCollection(%s): %v", ref.Name(), err)
		}
		wantDef, err := want.DescribeCollection(ctx, ref)
		if err != nil {
			t.Fatalf("existing reader DescribeCollection(%s): %v", ref.Name(), err)
		}
		if !reflect.DeepEqual(gotDef, wantDef) {
			t.Fatalf("DescribeCollection(%s):\nnarrow   = %+v\nexisting = %+v", ref.Name(), gotDef, wantDef)
		}

		gotIndexes, err := got.ListIndexes(ctx, ref)
		if err != nil {
			t.Fatalf("narrow reader ListIndexes(%s): %v", ref.Name(), err)
		}
		wantIndexes, err := want.ListIndexes(ctx, ref)
		if err != nil {
			t.Fatalf("existing reader ListIndexes(%s): %v", ref.Name(), err)
		}
		if !reflect.DeepEqual(gotIndexes, wantIndexes) {
			t.Fatalf("ListIndexes(%s): narrow = %+v, existing = %+v", ref.Name(), gotIndexes, wantIndexes)
		}

		gotConstraints, err := got.ListConstraints(ctx, ref)
		if err != nil {
			t.Fatalf("narrow reader ListConstraints(%s): %v", ref.Name(), err)
		}
		wantConstraints, err := want.ListConstraints(ctx, ref)
		if err != nil {
			t.Fatalf("existing reader ListConstraints(%s): %v", ref.Name(), err)
		}
		if !reflect.DeepEqual(gotConstraints, wantConstraints) {
			t.Fatalf("ListConstraints(%s): narrow = %+v, existing = %+v", ref.Name(), gotConstraints, wantConstraints)
		}

		gotReferrers, err := got.ListReferrers(ctx, ref)
		if err != nil {
			t.Fatalf("narrow reader ListReferrers(%s): %v", ref.Name(), err)
		}
		wantReferrers, err := want.ListReferrers(ctx, ref)
		if err != nil {
			t.Fatalf("existing reader ListReferrers(%s): %v", ref.Name(), err)
		}
		if !reflect.DeepEqual(gotReferrers, wantReferrers) {
			t.Fatalf("ListReferrers(%s): narrow = %+v, existing = %+v", ref.Name(), gotReferrers, wantReferrers)
		}
	}
}

func TestSchemaReaderConstructors_RejectNilSQLDB(t *testing.T) {
	for _, constructor := range []struct {
		name string
		new  func(*sql.DB) (dbschema.SchemaReader, error)
	}{
		{name: "strict", new: NewSchemaReader},
		{name: "lenient", new: NewLenientSchemaReader},
	} {
		t.Run(constructor.name, func(t *testing.T) {
			reader, err := constructor.new(nil)
			if reader != nil {
				t.Fatalf("reader = %T, want nil", reader)
			}
			if !errors.Is(err, errNilSQLDB) {
				t.Fatalf("err = %v, want errNilSQLDB", err)
			}
		})
	}
}

func TestNewSchemaReader_MatchesExistingStrictReader(t *testing.T) {
	sqlDB := openSharedMemorySQLDB(t)
	execAll(t, &Database{sqlDB: sqlDB},
		`CREATE TABLE authors (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, born_at DATETIME, rating NUMERIC(10,2))`,
		`CREATE TABLE books (id INTEGER PRIMARY KEY, author_id INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE, title TEXT NOT NULL, isbn TEXT UNIQUE, price REAL)`,
		`CREATE INDEX ix_books_author ON books(author_id)`,
		`CREATE VIEW recent_books AS SELECT id, title FROM books`,
		`INSERT INTO authors (name) VALUES ('Ada')`,
		`INSERT INTO books (id, author_id, title) VALUES (1, 1, 'Notes')`,
	)
	ctx := context.Background()

	reader, err := NewSchemaReader(sqlDB)
	if err != nil {
		t.Fatalf("NewSchemaReader: %v", err)
	}
	existing := &Database{sqlDB: sqlDB}
	assertSameSchemaReader(t, ctx, reader, existing, []*dal.CollectionRef{rootRef("authors"), rootRef("books")})

	// The strict reader lists no view and refuses to describe one.
	view := rootRef("recent_books")
	if _, err := reader.DescribeCollection(ctx, view); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("strict DescribeCollection(view) = %v, want not found", err)
	}

	// Reader operations leave the caller's pool and its data usable.
	if err := sqlDB.PingContext(ctx); err != nil {
		t.Fatalf("the reader must not close or break the borrowed pool: %v", err)
	}
	var books int
	if err := sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM books`).Scan(&books); err != nil {
		t.Fatalf("reading the borrowed pool after schema reads: %v", err)
	}
	if books != 1 {
		t.Fatalf("books = %d, want the seeded row still there", books)
	}
}

func TestNewLenientSchemaReader_PreservesLenientMetadata(t *testing.T) {
	sqlDB := openSharedMemorySQLDB(t)
	execAll(t, &Database{sqlDB: sqlDB},
		`CREATE TABLE things (id INTEGER PRIMARY KEY, doc JSON, untyped, a INT, computed INT GENERATED ALWAYS AS (a * 2) VIRTUAL)`,
		`CREATE INDEX ix_things_a ON things(a)`,
		`CREATE INDEX ix_things_computed ON things(computed)`,
		`CREATE VIEW things_view AS SELECT id, doc FROM things`,
		`CREATE VIRTUAL TABLE things_fts USING fts5(title, body)`,
	)
	ctx := context.Background()

	strict, err := NewSchemaReader(sqlDB)
	if err != nil {
		t.Fatalf("NewSchemaReader: %v", err)
	}
	lenient, err := NewLenientSchemaReader(sqlDB)
	if err != nil {
		t.Fatalf("NewLenientSchemaReader: %v", err)
	}
	existing := (&Database{sqlDB: sqlDB}).LenientSchema()
	assertSameSchemaReader(t, ctx, lenient, existing, []*dal.CollectionRef{
		rootRef("things"), rootRef("things_view"), rootRef("things_fts"),
	})

	// Unmapped declared types are Null leniently; generated and hidden columns are left out.
	things, err := lenient.DescribeCollection(ctx, rootRef("things"))
	if err != nil {
		t.Fatalf("lenient DescribeCollection(things): %v", err)
	}
	var fields []string
	for _, field := range things.Fields {
		fields = append(fields, string(field.Name))
	}
	if got := strings.Join(fields, ","); got != "id,doc,untyped,a" {
		t.Fatalf("lenient fields = %s, want the generated column left out", got)
	}
	for i, want := range []dbschema.Type{dbschema.Int, dbschema.Null, dbschema.Null, dbschema.Int} {
		if things.Fields[i].Type != want {
			t.Errorf("field %s type = %v, want %v", things.Fields[i].Name, things.Fields[i].Type, want)
		}
	}
	indexes, err := lenient.ListIndexes(ctx, rootRef("things"))
	if err != nil {
		t.Fatalf("lenient ListIndexes(things): %v", err)
	}
	if len(indexes) != 1 || indexes[0].Name != "ix_things_a" {
		t.Fatalf("lenient indexes = %+v, want only the index on a described column", indexes)
	}

	// The same reads stay refused without the lenient constructor.
	var notSupported *dbschema.NotSupportedError
	if _, err := strict.DescribeCollection(ctx, rootRef("things")); !errors.As(err, &notSupported) {
		t.Fatalf("strict DescribeCollection(things) = %v, want *dbschema.NotSupportedError", err)
	}
	if _, err := strict.DescribeCollection(ctx, rootRef("things_fts")); !errors.As(err, &notSupported) {
		t.Fatalf("strict DescribeCollection(things_fts) = %v, want *dbschema.NotSupportedError", err)
	}
	if _, err := strict.DescribeCollection(ctx, rootRef("things_view")); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("strict DescribeCollection(things_view) = %v, want not found", err)
	}
}

func TestNewSchemaReader_CallerCancellationKeepsBorrowedPool(t *testing.T) {
	sqlDB := openSharedMemorySQLDB(t)
	execAll(t, &Database{sqlDB: sqlDB}, `CREATE TABLE widget (id INTEGER PRIMARY KEY)`)
	reader, err := NewSchemaReader(sqlDB)
	if err != nil {
		t.Fatalf("NewSchemaReader: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.ListCollections(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ListCollections = %v, want context.Canceled", err)
	}
	if _, err := reader.DescribeCollection(ctx, rootRef("widget")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled DescribeCollection = %v, want context.Canceled", err)
	}

	if err := sqlDB.PingContext(context.Background()); err != nil {
		t.Fatalf("the borrowed pool must survive a cancelled read: %v", err)
	}
}

func TestSchemaReaderConstructors_ExposeOnlySchemaReader(t *testing.T) {
	for _, constructor := range []struct {
		name string
		new  func(*sql.DB) (dbschema.SchemaReader, error)
	}{
		{name: "strict", new: NewSchemaReader},
		{name: "lenient", new: NewLenientSchemaReader},
	} {
		t.Run(constructor.name, func(t *testing.T) {
			sqlDB := openSharedMemorySQLDB(t)
			reader, err := constructor.new(sqlDB)
			if err != nil {
				t.Fatalf("constructor: %v", err)
			}
			// The declared return type already guarantees dbschema.SchemaReader;
			// no other capability may be exposed.
			if _, ok := reader.(io.Closer); ok {
				t.Error("the schema reader must not close the caller's pool")
			}
			if _, ok := reader.(ddl.SchemaModifier); ok {
				t.Error("the schema reader must not expose DDL")
			}
			if _, ok := reader.(ddl.TransactionalDDL); ok {
				t.Error("the schema reader must not advertise DDL")
			}
			if _, ok := reader.(dbschema.SourceRowsReader); ok {
				t.Error("the schema reader must not expose source rows")
			}
			if _, ok := reader.(dbschema.SourceViewReader); ok {
				t.Error("the schema reader must not expose view listings")
			}
			if _, ok := reader.(dbschema.SourceConstraintChecker); ok {
				t.Error("the schema reader must not expose source data checks")
			}
			if _, ok := reader.(dal.DB); ok {
				t.Error("the schema reader must not expose the dal.DB surface")
			}
			if _, ok := reader.(*Database); ok {
				t.Error("the schema reader must be a narrowed value, not the Database")
			}
		})
	}
}
