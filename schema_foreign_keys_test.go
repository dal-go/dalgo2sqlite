package dalgo2sqlite

import (
	"context"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

func TestDescribeCollection_ForeignKeys(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, statement := range []string{
		`CREATE TABLE parent (a INTEGER, b INTEGER, PRIMARY KEY (a, b))`,
		`CREATE TABLE child (id INTEGER PRIMARY KEY, a INTEGER, b INTEGER,
		  FOREIGN KEY (a, b) REFERENCES parent(a, b) ON UPDATE RESTRICT ON DELETE CASCADE)`,
		`CREATE TABLE implicit_child (a INTEGER, b INTEGER,
		  FOREIGN KEY (a, b) REFERENCES parent)`,
	} {
		if _, err := db.sqlDB.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	ref := dal.NewRootCollectionRef("child", "")
	def, err := db.DescribeCollection(ctx, &ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(def.ForeignKeys) != 1 {
		t.Fatalf("foreign keys: %+v", def.ForeignKeys)
	}
	fk := def.ForeignKeys[0]
	if fk.ReferencedCollection != "parent" || len(fk.Fields) != 2 ||
		fk.Fields[0] != "a" || fk.Fields[1] != "b" ||
		len(fk.ReferencedFields) != 2 || fk.ReferencedFields[0] != "a" || fk.ReferencedFields[1] != "b" {
		t.Fatalf("composite foreign key: %+v", fk)
	}
	if fk.Enforcement != dbschema.ForeignKeyEnforcementDisabled {
		t.Fatalf("default enforcement: %s", fk.Enforcement)
	}
	if fk.OnUpdate != "RESTRICT" || fk.OnDelete != "CASCADE" {
		t.Fatalf("referential actions: %+v", fk)
	}
	if _, err := db.sqlDB.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	def, err = db.DescribeCollection(ctx, &ref)
	if err != nil {
		t.Fatal(err)
	}
	if def.ForeignKeys[0].Enforcement != dbschema.ForeignKeyEnforcementEnabled {
		t.Fatalf("enabled enforcement: %s", def.ForeignKeys[0].Enforcement)
	}
	implicit := dal.NewRootCollectionRef("implicit_child", "")
	def, err = db.DescribeCollection(ctx, &implicit)
	if err != nil {
		t.Fatal(err)
	}
	if len(def.ForeignKeys) != 1 || len(def.ForeignKeys[0].ReferencedFields) != 2 ||
		def.ForeignKeys[0].ReferencedFields[0] != "a" || def.ForeignKeys[0].ReferencedFields[1] != "b" {
		t.Fatalf("implicit target fields: %+v", def.ForeignKeys)
	}
}
