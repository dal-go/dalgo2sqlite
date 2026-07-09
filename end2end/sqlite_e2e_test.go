package end2end

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/dalgo2sqlite"
)

// TestE2E_CreateDescribeRoundTrip exercises the Feature's round-trip
// contract: CreateCollection writes the table; DescribeCollection
// reads the same shape back. AlterCollection coverage waits on the
// upstream dalgo ddl.Applier Feature.
func TestE2E_CreateDescribeRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := dalgo2sqlite.NewDatabase(filepath.Join(dir, "chinook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	c := dbschema.CollectionDef{
		Name: "tracks",
		Fields: []dbschema.FieldDef{
			{Name: dal.FieldName("track_id"), Type: dbschema.Int, AutoIncrement: true},
			{Name: dal.FieldName("name"), Type: dbschema.String, Nullable: false},
			{Name: dal.FieldName("milliseconds"), Type: dbschema.Int, Nullable: true},
			{Name: dal.FieldName("price"), Type: dbschema.Decimal, Nullable: false},
			{Name: dal.FieldName("created_at"), Type: dbschema.Time, Nullable: true},
		},
		PrimaryKey: []dal.FieldName{"track_id"},
		Indexes: []dbschema.IndexDef{
			{Name: "ix_tracks_name", Collection: "tracks", Fields: []dal.FieldName{"name"}},
		},
	}
	if err := ddl.CreateCollection(ctx, db, c); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	ref := dal.NewRootCollectionRef("tracks", "")
	got, err := db.DescribeCollection(ctx, &ref)
	if err != nil {
		t.Fatalf("DescribeCollection: %v", err)
	}
	if got.Name != "tracks" {
		t.Errorf("Name = %q, want tracks", got.Name)
	}
	if len(got.Fields) != 5 {
		t.Fatalf("Fields len = %d, want 5", len(got.Fields))
	}

	wantFields := []dbschema.FieldDef{
		{Name: "track_id", Type: dbschema.Int, AutoIncrement: true, Nullable: false},
		{Name: "name", Type: dbschema.String, Nullable: false},
		{Name: "milliseconds", Type: dbschema.Int, Nullable: true},
		{Name: "price", Type: dbschema.Decimal, Nullable: false},
		{Name: "created_at", Type: dbschema.Time, Nullable: true},
	}
	for i, want := range wantFields {
		g := got.Fields[i]
		if string(g.Name) != string(want.Name) {
			t.Errorf("Fields[%d].Name = %q, want %q", i, g.Name, want.Name)
		}
		if g.Type != want.Type {
			t.Errorf("Fields[%d].Type = %v, want %v (field %s)", i, g.Type, want.Type, g.Name)
		}
		if g.AutoIncrement != want.AutoIncrement {
			t.Errorf("Fields[%d].AutoIncrement = %v, want %v (field %s)", i, g.AutoIncrement, want.AutoIncrement, g.Name)
		}
		if g.Nullable != want.Nullable {
			t.Errorf("Fields[%d].Nullable = %v, want %v (field %s)", i, g.Nullable, want.Nullable, g.Name)
		}
	}
	if len(got.PrimaryKey) != 1 || string(got.PrimaryKey[0]) != "track_id" {
		t.Errorf("PrimaryKey = %v, want [track_id]", got.PrimaryKey)
	}
	if len(got.Indexes) != 1 || got.Indexes[0].Name != "ix_tracks_name" {
		t.Errorf("Indexes = %+v, want one entry named ix_tracks_name", got.Indexes)
	}
}

// TestE2E_MapDataRoundTrip verifies that map[string]any data works for
// Insert, Get, Set, and query round-trips via the dalgo2sqlite + dalgo2sql stack.
//
// Key requirements demonstrated here:
//
//   - Use [dalgo2sqlite.NewDatabaseWithOptions] with a dalgo2sql.DbOptions
//     that names the primary-key column via dalgo2sql.NewRecordset — without
//     this, Insert panics ("primary key is not defined").
//   - The table must be created via DDL before inserting (just like any SQL
//     database).
//   - Insert works with map[string]any; the key.ID supplies the PK column
//     value and the map supplies all non-PK columns.
//   - db.Get() with map[string]any data works: dalgo2sql issues SELECT *
//     and scans columns generically into the map (PK column included).
//   - db.Set() (upsert) with map[string]any works: inserts on first call,
//     updates on subsequent calls.
//   - WhereField queries via ExecuteQueryToRecordsReader work correctly.
func TestE2E_MapDataRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	// 1. Open DB with PK metadata for the "widgets" collection.
	db, err := dalgo2sqlite.NewDatabaseWithOptions(
		filepath.Join(dir, "widgets.db"),
		dal.NewSchema(nil, nil),
		dalgo2sql.DbOptions{
			Recordsets: map[string]*dalgo2sql.Recordset{
				"widgets": dalgo2sql.NewRecordset("widgets", dalgo2sql.Table,
					[]dal.FieldRef{dal.Field("id")}),
			},
		},
	)
	if err != nil {
		t.Fatalf("NewDatabaseWithOptions: %v", err)
	}
	defer func() { _ = db.Close() }()

	// 2. Create the table via DDL.
	collDef := dbschema.CollectionDef{
		Name: "widgets",
		Fields: []dbschema.FieldDef{
			{Name: dal.FieldName("id"), Type: dbschema.String, Nullable: false},
			{Name: dal.FieldName("name"), Type: dbschema.String, Nullable: false},
			{Name: dal.FieldName("price"), Type: dbschema.Decimal, Nullable: false},
		},
		PrimaryKey: []dal.FieldName{"id"},
	}
	if err := ddl.CreateCollection(ctx, db, collDef); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	// 3. Insert two records using map[string]any data.
	//    The key.ID becomes the PK column value; the map contains non-PK fields.
	rec1 := dal.NewRecordWithData(
		dal.NewKeyWithID("widgets", "w1"),
		map[string]any{"name": "Sprocket", "price": "9.99"},
	)
	if err := db.Insert(ctx, rec1); err != nil {
		t.Fatalf("Insert w1: %v", err)
	}
	rec2 := dal.NewRecordWithData(
		dal.NewKeyWithID("widgets", "w2"),
		map[string]any{"name": "Bolt", "price": "1.50"},
	)
	if err := db.Insert(ctx, rec2); err != nil {
		t.Fatalf("Insert w2: %v", err)
	}

	// 4. Query all records via ExecuteQueryToRecordsReader (the correct
	//    read path for map[string]any; db.Get with a map receiver does not
	//    work — see function doc).
	collRef := dal.NewRootCollectionRef("widgets", "")
	allQuery := dal.NewQueryBuilder(dal.From(&collRef)).SelectIntoRecordset()
	rr, err := db.ExecuteQueryToRecordsReader(ctx, allQuery)
	if err != nil {
		t.Fatalf("ExecuteQueryToRecordsReader: %v", err)
	}
	defer func() { _ = rr.Close() }()

	var allRecords []dal.Record
	for {
		rec, err := rr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("RecordsReader.Next: %v", err)
		}
		allRecords = append(allRecords, rec)
	}
	if len(allRecords) != 2 {
		t.Fatalf("expected 2 records, got %d", len(allRecords))
	}

	// 5. WhereField query — filter by name = "Sprocket".
	filteredQuery := dal.NewQueryBuilder(dal.From(&collRef)).
		WhereField("name", dal.Equal, "Sprocket").
		SelectIntoRecordset()
	fr, err := db.ExecuteQueryToRecordsReader(ctx, filteredQuery)
	if err != nil {
		t.Fatalf("ExecuteQueryToRecordsReader (filtered): %v", err)
	}
	defer func() { _ = fr.Close() }()

	filtered, err := fr.Next()
	if err != nil {
		t.Fatalf("filtered Next: %v", err)
	}
	data, ok := filtered.Data().(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any data, got %T", filtered.Data())
	}
	if data["name"] != "Sprocket" {
		t.Errorf("filtered record name = %v, want Sprocket", data["name"])
	}
	// Confirm no more rows.
	if _, err := fr.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("expected EOF after 1 filtered record, got %v", err)
	}

	// 6. db.Get() with map[string]any — previously broken, now fixed.
	//    Get returns SELECT * so the PK column is also present in the map.
	gotMap := make(map[string]any)
	getRec := dal.NewRecordWithData(dal.NewKeyWithID("widgets", "w1"), gotMap)
	if err := db.Get(ctx, getRec); err != nil {
		t.Fatalf("db.Get with map data: %v", err)
	}
	if gotMap["name"] != "Sprocket" {
		t.Errorf("db.Get map name = %v, want Sprocket", gotMap["name"])
	}
	if fmt.Sprintf("%v", gotMap["price"]) != "9.99" {
		t.Errorf("db.Get map price = %v (%T), want 9.99", gotMap["price"], gotMap["price"])
	}
	// PK column is included because SELECT * returns all columns.
	if gotMap["id"] != "w1" {
		t.Errorf("db.Get map id = %v, want w1", gotMap["id"])
	}

	// 7. db.Get() for a missing key must return dal.IsNotFound.
	missingMap := make(map[string]any)
	missingRec := dal.NewRecordWithData(dal.NewKeyWithID("widgets", "no-such-widget"), missingMap)
	if notFoundErr := db.Get(ctx, missingRec); notFoundErr == nil {
		t.Error("db.Get for missing key: expected not-found error, got nil")
	} else if !dal.IsNotFound(notFoundErr) {
		t.Errorf("db.Get for missing key: expected IsNotFound, got: %v", notFoundErr)
	}

	// 8. db.Set() upsert with map[string]any: insert new then update.
	setRec := dal.NewRecordWithData(
		dal.NewKeyWithID("widgets", "w3"),
		map[string]any{"name": "Gear", "price": "5.00"},
	)
	if err := db.Set(ctx, setRec); err != nil {
		t.Fatalf("db.Set (insert): %v", err)
	}
	// Verify via Get.
	gotSet := make(map[string]any)
	getSetRec := dal.NewRecordWithData(dal.NewKeyWithID("widgets", "w3"), gotSet)
	if err := db.Get(ctx, getSetRec); err != nil {
		t.Fatalf("db.Get after Set (insert): %v", err)
	}
	if gotSet["name"] != "Gear" {
		t.Errorf("name after Set insert = %v, want Gear", gotSet["name"])
	}
	// Now update via Set.
	updRec := dal.NewRecordWithData(
		dal.NewKeyWithID("widgets", "w3"),
		map[string]any{"name": "Big Gear", "price": "15.00"},
	)
	if err := db.Set(ctx, updRec); err != nil {
		t.Fatalf("db.Set (update): %v", err)
	}
	gotUpd := make(map[string]any)
	getUpdRec := dal.NewRecordWithData(dal.NewKeyWithID("widgets", "w3"), gotUpd)
	if err := db.Get(ctx, getUpdRec); err != nil {
		t.Fatalf("db.Get after Set (update): %v", err)
	}
	if gotUpd["name"] != "Big Gear" {
		t.Errorf("name after Set update = %v, want Big Gear", gotUpd["name"])
	}
	if fmt.Sprintf("%v", gotUpd["price"]) != "15" && fmt.Sprintf("%v", gotUpd["price"]) != "15.00" {
		t.Errorf("price after Set update = %v (%T), want 15 or 15.00", gotUpd["price"], gotUpd["price"])
	}
}
