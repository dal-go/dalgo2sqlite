package dalgo2sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
)

type widgetData struct {
	Name  string `dalgo:"name"`
	Price string `dalgo:"price"`
}

func TestDatabase_DAL_UnimplementedWhenNilBackend(t *testing.T) {
	d := &Database{}
	ctx := context.Background()
	key := record.NewKeyWithID("widgets", "w1")
	rec := record.NewRecordWithData(key, map[string]any{"name": "test"})

	if err := d.Set(ctx, rec); !errors.Is(err, dal.ErrNotImplementedYet) {
		t.Errorf("Set = %v, want ErrNotImplementedYet", err)
	}
	if err := d.SetMulti(ctx, []record.Record{rec}); !errors.Is(err, dal.ErrNotImplementedYet) {
		t.Errorf("SetMulti = %v, want ErrNotImplementedYet", err)
	}
	if err := d.Insert(ctx, rec); !errors.Is(err, dal.ErrNotImplementedYet) {
		t.Errorf("Insert = %v, want ErrNotImplementedYet", err)
	}
	if err := d.Upsert(ctx, rec); !errors.Is(err, dal.ErrNotImplementedYet) {
		t.Errorf("Upsert = %v, want ErrNotImplementedYet", err)
	}
	if err := d.Delete(ctx, key); !errors.Is(err, dal.ErrNotImplementedYet) {
		t.Errorf("Delete = %v, want ErrNotImplementedYet", err)
	}
	if err := d.DeleteMulti(ctx, []*record.Key{key}); !errors.Is(err, dal.ErrNotImplementedYet) {
		t.Errorf("DeleteMulti = %v, want ErrNotImplementedYet", err)
	}
	if err := d.Update(ctx, key, nil); !errors.Is(err, dal.ErrNotImplementedYet) {
		t.Errorf("Update = %v, want ErrNotImplementedYet", err)
	}
	if err := d.UpdateMulti(ctx, []*record.Key{key}, nil); !errors.Is(err, dal.ErrNotImplementedYet) {
		t.Errorf("UpdateMulti = %v, want ErrNotImplementedYet", err)
	}
	if err := d.UpdateRecord(ctx, rec, nil); !errors.Is(err, dal.ErrNotImplementedYet) {
		t.Errorf("UpdateRecord = %v, want ErrNotImplementedYet", err)
	}
}

func TestDatabase_DAL_Operations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	db, err := NewDatabaseWithOptions(
		filepath.Join(dir, "dal_ops.db"),
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

	collDef := dbschema.CollectionDef{
		Name: "widgets",
		Fields: []dbschema.FieldDef{
			{Name: dal.FieldName("id"), Type: dbschema.String, Nullable: false},
			{Name: dal.FieldName("name"), Type: dbschema.String, Nullable: false},
			{Name: dal.FieldName("price"), Type: dbschema.String, Nullable: false},
		},
		PrimaryKey: []dal.FieldName{"id"},
	}
	if err := ddl.CreateCollection(ctx, db, collDef); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	key1 := record.NewKeyWithID("widgets", "w1")
	data1 := &widgetData{Name: "Sprocket", Price: "1.23"}
	rec1 := record.NewRecordWithData(key1, data1)

	// Insert
	if err := db.Insert(ctx, rec1); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Get
	getRec1 := record.NewRecordWithData(key1, &widgetData{})
	if err := db.Get(ctx, getRec1); err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Exists
	if exists, err := db.Exists(ctx, key1); err != nil || !exists {
		t.Fatalf("Exists = (%v, %v), want (true, nil)", exists, err)
	}

	// Set
	data1.Price = "2.34"
	if err := db.Set(ctx, rec1); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// SetMulti
	key2 := record.NewKeyWithID("widgets", "w2")
	rec2 := record.NewRecordWithData(key2, &widgetData{Name: "Nut", Price: "0.50"})
	if err := db.SetMulti(ctx, []record.Record{rec2}); err != nil {
		t.Fatalf("SetMulti: %v", err)
	}

	// GetMulti
	getRec2 := record.NewRecordWithData(key2, &widgetData{})
	if err := db.GetMulti(ctx, []record.Record{getRec1, getRec2}); err != nil {
		t.Fatalf("GetMulti: %v", err)
	}

	// Upsert
	if err := db.Upsert(ctx, rec1); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Update
	if err := db.Update(ctx, key1, []update.Update{update.ByFieldName("price", "3.45")}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// UpdateMulti
	if err := db.UpdateMulti(ctx, []*record.Key{key1, key2}, []update.Update{update.ByFieldName("price", "4.56")}); err != nil {
		t.Fatalf("UpdateMulti: %v", err)
	}

	// ExecuteQueryToRecordsReader
	collRef := dal.NewRootCollectionRef("widgets", "")
	q := dal.NewQueryBuilder(dal.From(&collRef)).SelectIntoRecordset()
	reader, err := db.ExecuteQueryToRecordsReader(ctx, q)
	if err != nil {
		t.Fatalf("ExecuteQueryToRecordsReader: %v", err)
	}
	_ = reader.Close()

	// ExecuteQueryToRecordsetReader
	rsReader, err := db.ExecuteQueryToRecordsetReader(ctx, q)
	if err != nil {
		t.Fatalf("ExecuteQueryToRecordsetReader: %v", err)
	}
	_ = rsReader.Close()

	// RunReadonlyTransaction
	if err := db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		return nil
	}); err != nil {
		t.Fatalf("RunReadonlyTransaction: %v", err)
	}

	// Delete
	if err := db.Delete(ctx, key1); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// DeleteMulti
	if err := db.DeleteMulti(ctx, []*record.Key{key2}); err != nil {
		t.Fatalf("DeleteMulti: %v", err)
	}
}
