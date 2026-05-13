package dalgo2sqlite

import (
	"testing"

	"github.com/dal-go/dalgo/ddl"
)

func TestSupportsTransactionalDDL(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	defer func() { _ = db.Close() }()

	tx, ok := any(db).(ddl.TransactionalDDL)
	if !ok {
		t.Fatal("Database does not implement ddl.TransactionalDDL")
	}
	if !tx.SupportsTransactionalDDL() {
		t.Error("expected SupportsTransactionalDDL() = true for SQLite")
	}
}
