package dalgo2sqlite

import (
	"context"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
)

func TestCreateCollection_HappyPath(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	c := dbschema.CollectionDef{
		Name: "users",
		Fields: []dbschema.FieldDef{
			{Name: dal.FieldName("id"), Type: dbschema.Int, AutoIncrement: true},
			{Name: dal.FieldName("email"), Type: dbschema.String, Nullable: false},
			{Name: dal.FieldName("signup_at"), Type: dbschema.Time, Nullable: true},
		},
		PrimaryKey: []dal.FieldName{"id"},
		Indexes: []dbschema.IndexDef{
			{Name: "ix_users_email", Collection: "users", Fields: []dal.FieldName{"email"}},
		},
	}
	if err := ddl.CreateCollection(ctx, db, c); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	ref := dal.NewRootCollectionRef("users", "")
	got, err := db.DescribeCollection(ctx, &ref)
	if err != nil {
		t.Fatalf("DescribeCollection after Create: %v", err)
	}
	if len(got.Fields) != 3 {
		t.Errorf("Fields len = %d, want 3", len(got.Fields))
	}
	for _, f := range got.Fields {
		if f.Name == "signup_at" && f.Type != dbschema.Time {
			t.Errorf("signup_at Type = %v, want Time (marker should have been written)", f.Type)
		}
	}
	if len(got.PrimaryKey) != 1 || string(got.PrimaryKey[0]) != "id" {
		t.Errorf("PrimaryKey = %v, want [id]", got.PrimaryKey)
	}
	if len(got.Indexes) != 1 || got.Indexes[0].Name != "ix_users_email" {
		t.Errorf("Indexes = %+v, want one entry named ix_users_email", got.Indexes)
	}
}

func TestCreateCollection_IfNotExists(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	c := dbschema.CollectionDef{
		Name:       "users",
		Fields:     []dbschema.FieldDef{{Name: dal.FieldName("id"), Type: dbschema.Int}},
		PrimaryKey: []dal.FieldName{"id"},
	}
	if err := ddl.CreateCollection(ctx, db, c); err != nil {
		t.Fatalf("first CreateCollection: %v", err)
	}
	if err := ddl.CreateCollection(ctx, db, c, ddl.IfNotExists()); err != nil {
		t.Fatalf("CreateCollection with IfNotExists on existing table: %v", err)
	}
}

func TestCreateCollection_RollsBackOnFailure(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	c := dbschema.CollectionDef{
		Name: "users",
		Fields: []dbschema.FieldDef{
			{Name: dal.FieldName("id"), Type: dbschema.Int},
			{Name: dal.FieldName("email"), Type: dbschema.String},
		},
		PrimaryKey: []dal.FieldName{"id"},
		Indexes: []dbschema.IndexDef{
			{Name: "ix_users_email", Collection: "users", Fields: []dal.FieldName{"email"}},
			{Name: "ix_users_email", Collection: "users", Fields: []dal.FieldName{"email"}}, // dup → SQLite errors
		},
	}
	err := ddl.CreateCollection(ctx, db, c)
	if err == nil {
		t.Fatal("expected error from duplicate index name, got nil")
	}
	tables, _ := db.ListCollections(ctx, nil)
	for _, t2 := range tables {
		if t2.Name() == "users" {
			t.Errorf("expected rollback to drop the users table; it still exists")
		}
	}
}

func TestDropCollection_Cascade(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	c := dbschema.CollectionDef{
		Name:       "users",
		Fields:     []dbschema.FieldDef{{Name: dal.FieldName("id"), Type: dbschema.Int}},
		PrimaryKey: []dal.FieldName{"id"},
	}
	if err := ddl.CreateCollection(ctx, db, c); err != nil {
		t.Fatal(err)
	}
	if err := ddl.DropCollection(ctx, db, "users"); err != nil {
		t.Fatalf("DropCollection: %v", err)
	}
	tables, _ := db.ListCollections(ctx, nil)
	for _, t2 := range tables {
		if t2.Name() == "users" {
			t.Errorf("users still listed after DropCollection")
		}
	}

	// IfExists tolerates a missing table.
	if err := ddl.DropCollection(ctx, db, "nonexistent", ddl.IfExists()); err != nil {
		t.Errorf("DropCollection IfExists on missing table: %v", err)
	}
}
