package dalgo2sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
)

func TestListConstraints_PKAndFK(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	for _, stmt := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER, FOREIGN KEY(user_id) REFERENCES users(id))`,
	} {
		if _, err := db.sqlDB.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	ordersRef := dal.NewRootCollectionRef("orders", "")
	got, err := db.ListConstraints(ctx, &ordersRef)
	if err != nil {
		t.Fatalf("ListConstraints: %v", err)
	}
	var pk, fk int
	for _, c := range got {
		switch c.Type {
		case "primary-key":
			pk++
		case "foreign-key":
			fk++
		}
	}
	if pk != 1 {
		t.Errorf("expected exactly 1 primary-key constraint, got %d (constraints: %+v)", pk, got)
	}
	if fk != 1 {
		t.Errorf("expected exactly 1 foreign-key constraint, got %d (constraints: %+v)", fk, got)
	}
}

func TestListReferrers(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	for _, stmt := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER, FOREIGN KEY(user_id) REFERENCES users(id))`,
		`CREATE TABLE audits (id INTEGER PRIMARY KEY, user_id INTEGER, FOREIGN KEY(user_id) REFERENCES users(id))`,
	} {
		if _, err := db.sqlDB.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	usersRef := dal.NewRootCollectionRef("users", "")
	got, err := db.ListReferrers(ctx, &usersRef)
	if err != nil {
		t.Fatalf("ListReferrers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 referrers (orders, audits), got %d: %+v", len(got), got)
	}
	wantTables := map[string]bool{"orders": false, "audits": false}
	for _, r := range got {
		_, known := wantTables[r.Collection.Name()]
		if !known {
			t.Errorf("unexpected referrer table %q", r.Collection.Name())
			continue
		}
		wantTables[r.Collection.Name()] = true
		if len(r.Fields) != 1 || string(r.Fields[0]) != "user_id" {
			t.Errorf("referrer %q fields = %v, want [user_id]", r.Collection.Name(), r.Fields)
		}
	}
	for n, seen := range wantTables {
		if !seen {
			t.Errorf("expected referrer %q not found", n)
		}
	}
}

func TestListConstraints_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ref := dal.NewRootCollectionRef("users", "")

	t.Run("pk_probe_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_table_info") {
					return nil, errors.New("pk probe failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.ListConstraints(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("fk_probe_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_foreign_key_list") {
					return nil, errors.New("fk probe failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.ListConstraints(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("fk_scan_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_foreign_key_list") {
					return &mockRows{cols: []string{"id"}, rows: [][]driver.Value{{struct{}{}}}}, nil, true
				}
				return nil, nil, false
			},
		})
		if _, err := db.ListConstraints(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestListReferrers_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ref := dal.NewRootCollectionRef("users", "")

	t.Run("list_collections_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "sqlite_master") {
					return nil, errors.New("list collections failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.ListReferrers(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("referrer_fields_query_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_foreign_key_list") {
					return nil, errors.New("referrer fields failed"), true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE orders (id INT)"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ListReferrers(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("referrer_fields_scan_error", func(t *testing.T) {
		_, db := newHookDB(t, &driverHooks{
			queryHook: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error, bool) {
				if strings.Contains(q, "pragma_foreign_key_list") {
					return &mockRows{cols: []string{"from"}, rows: [][]driver.Value{{struct{}{}}}}, nil, true
				}
				return nil, nil, false
			},
		})
		if _, err := db.sqlDB.ExecContext(ctx, "CREATE TABLE orders (id INT)"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ListReferrers(ctx, &ref); err == nil {
			t.Fatal("expected error")
		}
	})
}

