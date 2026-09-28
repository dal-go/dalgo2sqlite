package dalgo2sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
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

func TestCreateCollection_EdgeCasesAndErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("invalid_field_type", func(t *testing.T) {
		db := openTestDB(t)
		c := dbschema.CollectionDef{
			Name:   "users",
			Fields: []dbschema.FieldDef{{Name: "bad", Type: dbschema.Null}},
		}
		if err := db.CreateCollection(ctx, c); err == nil {
			t.Fatal("expected error for invalid field type in CreateCollection")
		}
	})

	t.Run("index_empty_name", func(t *testing.T) {
		db := openTestDB(t)
		c := dbschema.CollectionDef{
			Name:   "users",
			Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
			Indexes: []dbschema.IndexDef{
				{Name: "", Fields: []dal.FieldName{"id"}},
			},
		}
		if err := db.CreateCollection(ctx, c); err == nil {
			t.Fatal("expected error for empty index name")
		}
	})

	t.Run("index_empty_collection_resolved", func(t *testing.T) {
		db := openTestDB(t)
		c := dbschema.CollectionDef{
			Name:   "users",
			Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
			Indexes: []dbschema.IndexDef{
				{Name: "ix_id", Collection: "", Fields: []dal.FieldName{"id"}},
			},
		}
		if err := db.CreateCollection(ctx, c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("create_table_exec_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.HasPrefix(q, "CREATE TABLE users") {
					return nil, errors.New("create table failed"), true
				}
				return nil, nil, false
			},
		})
		c := dbschema.CollectionDef{
			Name:   "users",
			Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
		}
		if err := db.CreateCollection(ctx, c); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("time_marker_table_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.Contains(q, "CREATE TABLE IF NOT EXISTS _dalgo_time_columns") {
					return nil, errors.New("marker table failed"), true
				}
				return nil, nil, false
			},
		})
		c := dbschema.CollectionDef{
			Name:   "users",
			Fields: []dbschema.FieldDef{{Name: "created_at", Type: dbschema.Time}},
		}
		if err := db.CreateCollection(ctx, c); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("time_marker_insert_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.Contains(q, "INSERT OR IGNORE INTO _dalgo_time_columns") {
					return nil, errors.New("marker insert failed"), true
				}
				return nil, nil, false
			},
		})
		c := dbschema.CollectionDef{
			Name:   "users",
			Fields: []dbschema.FieldDef{{Name: "created_at", Type: dbschema.Time}},
		}
		if err := db.CreateCollection(ctx, c); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestInTx_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("begin_tx_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			beginHook: func(ctx context.Context, opts driver.TxOptions) (driver.Tx, error, bool) {
				return nil, errors.New("begin failed"), true
			},
		})
		if err := db.DropCollection(ctx, "users"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("commit_tx_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			commitHook: func() (error, bool) {
				return errors.New("commit failed"), true
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.DropCollection(ctx, "users"); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestDropCollection_Error(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, db := newHookDB(t, &driverHooks{
		execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
			if strings.HasPrefix(q, "DROP TABLE") {
				return nil, errors.New("drop failed"), true
			}
			return nil, nil, false
		},
	})
	if err := db.DropCollection(ctx, "users"); err == nil {
		t.Fatal("expected error")
	}
}

func TestAlterCollection_ErrorsAndEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("op_apply_error", func(t *testing.T) {
		db := openTestDB(t)
		if err := db.AlterCollection(ctx, "users", ddl.AddField(dbschema.FieldDef{Name: "bad", Type: dbschema.Null})); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("add_field_exec_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.Contains(q, "ADD COLUMN") {
					return nil, errors.New("add column failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.AddField(dbschema.FieldDef{Name: "age", Type: dbschema.Int})); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("add_field_time_marker_table_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.Contains(q, "CREATE TABLE IF NOT EXISTS _dalgo_time_columns") {
					return nil, errors.New("marker table failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.AddField(dbschema.FieldDef{Name: "ts", Type: dbschema.Time})); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("add_field_time_marker_insert_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.Contains(q, "INSERT OR IGNORE INTO _dalgo_time_columns") {
					return nil, errors.New("marker insert failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.AddField(dbschema.FieldDef{Name: "ts", Type: dbschema.Time})); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("add_field_time_happy_path", func(t *testing.T) {
		db := openTestDB(t)
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.AddField(dbschema.FieldDef{Name: "ts", Type: dbschema.Time})); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("drop_field_exec_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.Contains(q, "DROP COLUMN") {
					return nil, errors.New("drop column failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT, age INT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.DropField(dal.FieldName("age"))); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("rename_field_exec_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.Contains(q, "RENAME COLUMN") {
					return nil, errors.New("rename column failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT, email TEXT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.RenameField(dal.FieldName("email"), dal.FieldName("mail"))); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("add_index_empty_collection", func(t *testing.T) {
		db := openTestDB(t)
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT, email TEXT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.AddIndex(dbschema.IndexDef{
			Name: "ix_mail", Collection: "", Fields: []dal.FieldName{"email"},
		})); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("add_index_invalid_index", func(t *testing.T) {
		db := openTestDB(t)
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT, email TEXT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.AddIndex(dbschema.IndexDef{
			Name: "", Fields: []dal.FieldName{"email"},
		})); err == nil {
			t.Fatal("expected error for empty index name")
		}
	})

	t.Run("add_index_exec_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.Contains(q, "CREATE INDEX") {
					return nil, errors.New("create index failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT, email TEXT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.AddIndex(dbschema.IndexDef{
			Name: "ix_email", Fields: []dal.FieldName{"email"},
		})); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("drop_index_exec_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.Contains(q, "DROP INDEX") {
					return nil, errors.New("drop index failed"), true
				}
				return nil, nil, false
			},
		})
		if err := db.AlterCollection(ctx, "users", ddl.DropIndex("ix_email")); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("modify_field_introspect_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_table_info") {
					return nil, errors.New("table info failed"), true
				}
				return nil, nil, false
			},
		})
		if err := db.AlterCollection(ctx, "users", ddl.ModifyField(dal.FieldName("email"), dbschema.FieldDef{Type: dbschema.String})); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("modify_field_column_not_found", func(t *testing.T) {
		db := openTestDB(t)
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT PRIMARY KEY, email TEXT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.ModifyField(dal.FieldName("missing"), dbschema.FieldDef{Type: dbschema.String})); err == nil {
			t.Fatal("expected error for nonexistent column")
		}
	})

	t.Run("modify_field_build_create_table_error", func(t *testing.T) {
		db := openTestDB(t)
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT PRIMARY KEY, email TEXT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.ModifyField(dal.FieldName("email"), dbschema.FieldDef{Type: dbschema.Null})); err == nil {
			t.Fatal("expected error for invalid type")
		}
	})

	t.Run("modify_field_create_new_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.Contains(q, "CREATE TABLE users_new") {
					return nil, errors.New("create new failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT PRIMARY KEY, email TEXT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.ModifyField(dal.FieldName("email"), dbschema.FieldDef{Type: dbschema.String})); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("modify_field_copy_data_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.Contains(q, "INSERT INTO users_new") {
					return nil, errors.New("copy data failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT PRIMARY KEY, email TEXT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.ModifyField(dal.FieldName("email"), dbschema.FieldDef{Type: dbschema.String})); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("modify_field_drop_original_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if q == "DROP TABLE users" {
					return nil, errors.New("drop original failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT PRIMARY KEY, email TEXT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.ModifyField(dal.FieldName("email"), dbschema.FieldDef{Type: dbschema.String})); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("modify_field_rename_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			execHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error, bool) {
				if strings.Contains(q, "ALTER TABLE users_new RENAME TO users") {
					return nil, errors.New("rename failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (id INT PRIMARY KEY, email TEXT)"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.ModifyField(dal.FieldName("email"), dbschema.FieldDef{Type: dbschema.String})); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("modify_field_read_collection_scan_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_table_info") {
					return &mockRows{
						cols: []string{"name", "type", "notnull", "pk"},
						rows: [][]driver.Value{{struct{}{}}},
					}, nil, true
				}
				return nil, nil, false
			},
		})
		if err := db.AlterCollection(ctx, "users", ddl.ModifyField(dal.FieldName("email"), dbschema.FieldDef{Type: dbschema.String})); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("modify_field_with_unknown_type_and_multi_pk", func(t *testing.T) {
		db := openTestDB(t)
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE users (a INT, b INT, email TEXT, custom FOOBAR, PRIMARY KEY (b, a))"); err != nil {
			t.Fatal(err)
		}
		if err := db.AlterCollection(ctx, "users", ddl.ModifyField(dal.FieldName("email"), dbschema.FieldDef{Type: dbschema.String, Nullable: false})); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

