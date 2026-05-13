package dalgo2sqlite

import (
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
)

func buildCreateTableSQL(c dbschema.CollectionDef, opts ddl.Options) (string, error) {
	var sb strings.Builder
	sb.WriteString("CREATE TABLE ")
	if opts.IfNotExists {
		sb.WriteString("IF NOT EXISTS ")
	}
	sb.WriteString(c.Name)
	sb.WriteString(" (")

	inlinePK := len(c.PrimaryKey) == 1 && fieldHasAutoIncIntPK(c, c.PrimaryKey[0])
	compositePK := len(c.PrimaryKey) > 1

	// Build a set of PK field names for quick lookup.
	pkSet := make(map[dal.FieldName]bool, len(c.PrimaryKey))
	for _, n := range c.PrimaryKey {
		pkSet[n] = true
	}

	parts := make([]string, 0, len(c.Fields)+1)
	for _, f := range c.Fields {
		colSQL, err := buildColumnDecl(f, inlinePK && f.Name == c.PrimaryKey[0], compositePK, pkSet[f.Name])
		if err != nil {
			return "", err
		}
		parts = append(parts, colSQL)
	}
	if !inlinePK && len(c.PrimaryKey) > 0 {
		pkNames := make([]string, len(c.PrimaryKey))
		for i, n := range c.PrimaryKey {
			pkNames[i] = string(n)
		}
		parts = append(parts, "PRIMARY KEY ("+strings.Join(pkNames, ", ")+")")
	}
	sb.WriteString(strings.Join(parts, ", "))
	sb.WriteString(")")
	return sb.String(), nil
}

// buildColumnDecl renders one column declaration.
// inlinePK: this field is the sole autoincrement integer PK (handled inline).
// compositePK: the table has a multi-field primary key defined at table level.
// inPK: this field is a member of the primary key.
func buildColumnDecl(f dbschema.FieldDef, inlinePK, compositePK, inPK bool) (string, error) {
	sqlType, err := sqliteTypeFor(f.Type)
	if err != nil {
		return "", fmt.Errorf("dalgo2sqlite: field %q: %w", f.Name, err)
	}
	parts := []string{string(f.Name), sqlType}
	if inlinePK {
		parts = append(parts, "PRIMARY KEY")
		if f.AutoIncrement {
			parts = append(parts, "AUTOINCREMENT")
		}
		return strings.Join(parts, " "), nil
	}
	// For composite-PK tables: only PK members get NOT NULL.
	// For non-composite-PK tables: all non-nullable fields get NOT NULL.
	emitNotNull := !f.Nullable && (!compositePK || inPK)
	if emitNotNull {
		parts = append(parts, "NOT NULL")
	}
	return strings.Join(parts, " "), nil
}

func fieldHasAutoIncIntPK(c dbschema.CollectionDef, name dal.FieldName) bool {
	for _, f := range c.Fields {
		if f.Name == name {
			return f.Type == dbschema.Int && f.AutoIncrement
		}
	}
	return false
}

func buildCreateIndexSQL(idx dbschema.IndexDef, opts ddl.Options) (string, error) {
	if idx.Name == "" {
		return "", fmt.Errorf("dalgo2sqlite: index name cannot be empty")
	}
	if idx.Collection == "" {
		return "", fmt.Errorf("dalgo2sqlite: index %q: collection cannot be empty", idx.Name)
	}
	if len(idx.Fields) == 0 {
		return "", fmt.Errorf("dalgo2sqlite: index %q: must have at least one field", idx.Name)
	}
	var sb strings.Builder
	sb.WriteString("CREATE ")
	if idx.Unique {
		sb.WriteString("UNIQUE ")
	}
	sb.WriteString("INDEX ")
	if opts.IfNotExists {
		sb.WriteString("IF NOT EXISTS ")
	}
	sb.WriteString(idx.Name)
	sb.WriteString(" ON ")
	sb.WriteString(idx.Collection)
	sb.WriteString(" (")
	cols := make([]string, len(idx.Fields))
	for i, n := range idx.Fields {
		cols[i] = string(n)
	}
	sb.WriteString(strings.Join(cols, ", "))
	sb.WriteString(")")
	return sb.String(), nil
}
