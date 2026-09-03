package end2end

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/dal-go/record"
)

type customer struct {
	Name       string
	AssignedTo string
}

// TestE2E_AccessConditionsReachSQL proves that a row-level access condition
// reaches the SQL this adapter runs: dalgo2sql emits SQL from the query's
// String(), so a policy residual that only lived on Where() would be lost and
// every row returned. It also proves that a quote inside a policy variable
// cannot widen the query.
func TestE2E_AccessConditionsReachSQL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	options := dalgo2sql.DbOptions{
		Recordsets: map[string]*dalgo2sql.Recordset{
			"Customers": dalgo2sql.NewRecordset("Customers", dalgo2sql.Table, []dal.FieldRef{dal.Field("ID")}),
		},
	}
	db, err := dalgo2sqlite.NewDatabaseWithOptions(filepath.Join(t.TempDir(), "access.db"), dalgo2sql.NewSimpleSchema("ID"), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := ddl.CreateCollection(ctx, db, dbschema.CollectionDef{
		Name: "Customers",
		Fields: []dbschema.FieldDef{
			{Name: "ID", Type: dbschema.String},
			{Name: "Name", Type: dbschema.String},
			{Name: "AssignedTo", Type: dbschema.String},
		},
		PrimaryKey: []dal.FieldName{"ID"},
	}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	seed := map[string]customer{
		"c1": {Name: "Ann", AssignedTo: "u1"},
		"c2": {Name: "Bob", AssignedTo: "u2"},
		"c3": {Name: "O'Hare", AssignedTo: "u1"},
	}
	if err := db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		for id, c := range seed {
			c := c
			if err := tx.Insert(ctx, record.NewRecordWithData(record.NewKeyWithID("Customers", id), &c)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	policy := access.MustPolicy("customers",
		access.Collection("Customers", access.Allow(access.Query, "assigned-only").Where(
			dal.WhereField("AssignedTo", dal.Equal, dal.NewParam("currentUser")))),
	)
	secured := access.MustSecureDB(db, access.WithDatabasePolicies(policy))
	newRecord := func() record.Record {
		return record.NewRecordWithData(record.NewKeyWithID("Customers", ""), map[string]any{})
	}
	names := func(t *testing.T, ctx context.Context, q dal.StructuredQuery) []string {
		t.Helper()
		records, err := dal.ExecuteQueryAndReadAllToRecords(ctx, q, secured)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		var out []string
		for _, rec := range records {
			out = append(out, rec.Data().(map[string]any)["Name"].(string))
		}
		sort.Strings(out)
		return out
	}
	equal := func(t *testing.T, want, got []string) {
		t.Helper()
		if len(want) != len(got) {
			t.Fatalf("rows = %v, want %v", got, want)
		}
		for i := range want {
			if want[i] != got[i] {
				t.Fatalf("rows = %v, want %v", got, want)
			}
		}
	}
	base := func() dal.IQueryBuilder { return dal.From(dal.NewRootCollectionRef("Customers", "")).NewQuery() }
	user1 := access.WithCurrentUser(ctx, "u1")

	t.Run("residual_reaches_the_sql", func(t *testing.T) {
		equal(t, []string{"Ann", "O'Hare"}, names(t, user1, base().SelectIntoRecord(newRecord)))
	})
	t.Run("caller_condition_and_residual_conjoin", func(t *testing.T) {
		equal(t, []string{"Ann"}, names(t, user1, base().WhereField("Name", dal.Equal, "Ann").SelectIntoRecord(newRecord)))
	})
	t.Run("quoted_literals_are_escaped", func(t *testing.T) {
		equal(t, []string{"O'Hare"}, names(t, user1, base().WhereField("Name", dal.Equal, "O'Hare").SelectIntoRecord(newRecord)))
		injected := access.WithCurrentUser(ctx, "u1' OR '1'='1")
		equal(t, nil, names(t, injected, base().SelectIntoRecord(newRecord)))
	})
	t.Run("missing_variable_denies", func(t *testing.T) {
		_, err := secured.ExecuteQueryToRecordsReader(ctx, base().SelectIntoRecord(newRecord))
		if !errors.Is(err, access.ErrAccessDenied) {
			t.Fatalf("expected access denied, got %v", err)
		}
	})
}
