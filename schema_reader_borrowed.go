package dalgo2sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
)

// errNilSQLDB is returned by the constructors that take a caller-supplied
// *sql.DB when there is none to borrow.
var errNilSQLDB = errors.New("dalgo2sqlite: nil *sql.DB")

// NewSchemaReader returns a read-only [dbschema.SchemaReader] that introspects
// the SQLite schema reachable through sqlDB — a pool the caller opened and
// owns. It opens no file, creates no pool, and does not ping: every SQLite
// query runs on the caller's pool under the caller's context, and a fault is
// reported by the read that hits it. The reader borrows the pool and never
// closes it; the caller must keep sqlDB open while the reader is in use.
//
// The returned value is narrowed to [dbschema.SchemaReader]: it is not a
// [dal.DB], exposes no DDL, row, or Close surface, so a runtime capability
// assertion cannot reach a write or a teardown. Its metadata is the strict
// kind a [Database] reports, including refusal (a *dbschema.NotSupportedError)
// of a schema a lossless export cannot carry. Use [NewLenientSchemaReader] for
// the [Database.LenientSchema] view.
//
// sqlDB is typically a pool to a database this package did not open, such as
// an in-memory SQLite database or a remotely brokered connection.
func NewSchemaReader(sqlDB *sql.DB) (dbschema.SchemaReader, error) {
	return newSchemaReader(sqlDB, false)
}

// NewLenientSchemaReader is like [NewSchemaReader] but its schema reads
// describe the database the way SQLite lists it, with the same distinctions
// [Database.LenientSchema] makes: views are describable, a column whose
// declared type has no portable type is [dbschema.Null] instead of an error,
// and generated or hidden columns are left out instead of refused. It borrows
// sqlDB on the same terms: the caller owns the pool and its teardown.
func NewLenientSchemaReader(sqlDB *sql.DB) (dbschema.SchemaReader, error) {
	return newSchemaReader(sqlDB, true)
}

func newSchemaReader(sqlDB *sql.DB, lenient bool) (dbschema.SchemaReader, error) {
	if sqlDB == nil {
		return nil, errNilSQLDB
	}
	return &borrowedSchemaReader{db: &Database{sqlDB: sqlDB, lenient: lenient}}, nil
}

// borrowedSchemaReader is the private narrowing of a [Database] to the five
// [dbschema.SchemaReader] methods. Its Database is built only to hold the
// borrowed handle and the strict/lenient switch; none of the reader methods
// touches the dal.DB field, and the value is never exposed, so the DDL,
// source-row, and Close methods of Database stay unreachable.
type borrowedSchemaReader struct {
	db *Database
}

func (r *borrowedSchemaReader) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	return r.db.ListCollections(ctx, parent)
}

func (r *borrowedSchemaReader) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return r.db.DescribeCollection(ctx, ref)
}

func (r *borrowedSchemaReader) ListIndexes(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return r.db.ListIndexes(ctx, ref)
}

func (r *borrowedSchemaReader) ListConstraints(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return r.db.ListConstraints(ctx, ref)
}

func (r *borrowedSchemaReader) ListReferrers(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return r.db.ListReferrers(ctx, ref)
}
