package dalgo2sqlite

import (
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dalgotest"
	"github.com/dal-go/dalgo2sql"
)

// TestConformance runs the shared dalgotest suite against a fresh SQLite
// file database (modernc.org/sqlite, pure Go) — no external service and no
// env-gate needed, the same as every other test in this package.
//
// dalgo2sqlite itself validates nothing beyond what dalgo2sql provides.
// Every check here passes because dal.NewDB's write pipeline runs
// BeforeSave validation and hooks before RunReadwriteTransaction ever
// reaches dalgo2sql's transaction, and RunReadwriteTransaction is the path
// every check in this suite writes through — see database_dal.go's
// RunReadwriteTransaction and dalgo2sql's own conformance_test.go.
func TestConformance(t *testing.T) {
	opts := dalgo2sql.DbOptions{
		Recordsets: map[string]*dalgo2sql.Recordset{
			dalgotest.DefaultCollection: dalgo2sql.NewRecordset(
				dalgotest.DefaultCollection, dalgo2sql.Table, []dal.FieldRef{dal.Field("ID")},
			),
		},
	}

	dalgotest.RunConformance(t, func(t *testing.T) (dal.DB, func()) {
		db, err := NewDatabaseWithOptions(
			filepath.Join(t.TempDir(), "conformance.db"), dal.NewSchema(nil, nil), opts)
		if err != nil {
			t.Fatalf("NewDatabaseWithOptions: %v", err)
		}
		if _, err := db.sqlDB.Exec(`CREATE TABLE ` + dalgotest.DefaultCollection + ` (
			ID   TEXT PRIMARY KEY,
			Name TEXT
		)`); err != nil {
			t.Fatalf("CREATE TABLE: %v", err)
		}
		return db, func() { _ = db.Close() }
	})
}
