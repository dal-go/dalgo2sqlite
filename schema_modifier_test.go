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

func TestAlterCollection_AddDropRenameField(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	c := dbschema.CollectionDef{
		Name:       "users",
		Fields:     []dbschema.FieldDef{{Name: dal.FieldName("id"), Type: dbschema.Int}, {Name: dal.FieldName("email"), Type: dbschema.String}},
		PrimaryKey: []dal.FieldName{"id"},
	}
	if err := ddl.CreateCollection(ctx, db, c); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sqlDB.ExecContext(ctx, `INSERT INTO users(id, email) VALUES(1, 'alice@example.com'),(2, 'bob@example.com')`); err != nil {
		t.Fatal(err)
	}

	err := ddl.AlterCollection(ctx, db, "users",
		ddl.AddField(dbschema.FieldDef{Name: dal.FieldName("age"), Type: dbschema.Int, Nullable: true}),
		ddl.RenameField(dal.FieldName("email"), dal.FieldName("email_address")),
		ddl.DropField(dal.FieldName("age")),
	)
	if err != nil {
		t.Fatalf("AlterCollection: %v", err)
	}

	ref := dal.NewRootCollectionRef("users", "")
	got, err := db.DescribeCollection(ctx, &ref)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool)
	for _, f := range got.Fields {
		names[string(f.Name)] = true
	}
	if !names["id"] || !names["email_address"] {
		t.Errorf("expected fields id + email_address, got %+v", names)
	}
	if names["email"] || names["age"] {
		t.Errorf("unexpected residual fields after alter; got %+v", names)
	}

	var count int
	if err := db.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows after alter, got %d", count)
	}
}

func TestAlterCollection_AddDropIndex(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	c := dbschema.CollectionDef{
		Name:       "users",
		Fields:     []dbschema.FieldDef{{Name: dal.FieldName("id"), Type: dbschema.Int}, {Name: dal.FieldName("email"), Type: dbschema.String}},
		PrimaryKey: []dal.FieldName{"id"},
	}
	if err := ddl.CreateCollection(ctx, db, c); err != nil {
		t.Fatal(err)
	}

	addIdx := dbschema.IndexDef{Name: "ix_users_email", Collection: "users", Fields: []dal.FieldName{"email"}}
	if err := ddl.AlterCollection(ctx, db, "users", ddl.AddIndex(addIdx), ddl.DropIndex("ix_users_email")); err != nil {
		t.Fatalf("AlterCollection: %v", err)
	}

	ref := dal.NewRootCollectionRef("users", "")
	idxs, _ := db.ListIndexes(ctx, &ref)
	if len(idxs) != 0 {
		t.Errorf("expected no remaining indexes after add+drop, got %+v", idxs)
	}
}

func TestAlterCollection_ModifyFieldPreservesData(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	c := dbschema.CollectionDef{
		Name:       "users",
		Fields:     []dbschema.FieldDef{{Name: dal.FieldName("id"), Type: dbschema.Int}, {Name: dal.FieldName("email"), Type: dbschema.String, Nullable: true}},
		PrimaryKey: []dal.FieldName{"id"},
	}
	if err := ddl.CreateCollection(ctx, db, c); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sqlDB.ExecContext(ctx, `INSERT INTO users(id, email) VALUES(1, 'a@a'),(2, 'b@b'),(3, 'c@c')`); err != nil {
		t.Fatal(err)
	}

	newDef := dbschema.FieldDef{Name: dal.FieldName("email"), Type: dbschema.String, Nullable: false}
	if err := ddl.AlterCollection(ctx, db, "users", ddl.ModifyField(dal.FieldName("email"), newDef)); err != nil {
		t.Fatalf("ModifyField: %v", err)
	}

	ref := dal.NewRootCollectionRef("users", "")
	got, _ := db.DescribeCollection(ctx, &ref)
	var emailFound bool
	for _, f := range got.Fields {
		if f.Name == "email" {
			emailFound = true
			if f.Nullable {
				t.Errorf("email Nullable = true after modify, want false")
			}
		}
	}
	if !emailFound {
		t.Error("email column missing after ModifyField")
	}
	var count int
	_ = db.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	if count != 3 {
		t.Errorf("expected 3 rows preserved through migration dance, got %d", count)
	}
}
