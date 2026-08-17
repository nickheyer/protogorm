package migrate

import (
	"fmt"
	"slices"
	"strings"

	"gorm.io/gorm"
)

// Engine bookkeeping tables never join the schema shape
var internalTables = []string{LedgerTable, "sqlite_sequence"}

// Reads the live schema through gorm introspection
// Shape mirrors SpecOf so fingerprints compare cleanly
func SpecOfDB(db *gorm.DB) (*Spec, error) {
	d, err := DialectFor(db)
	if err != nil {
		return nil, err
	}
	migrator := db.Migrator()
	tables, err := migrator.GetTables()
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}

	spec := &Spec{}
	for _, name := range tables {
		if slices.Contains(internalTables, name) {
			continue
		}
		table := &TableSpec{Name: name}

		cols, err := migrator.ColumnTypes(name)
		if err != nil {
			return nil, fmt.Errorf("columns of %s: %w", name, err)
		}
		for _, col := range cols {
			cs := &ColumnSpec{
				Name:  col.Name(),
				Types: map[string]string{d.Name(): col.DatabaseTypeName()},
			}
			if nullable, ok := col.Nullable(); ok {
				cs.NotNull = !nullable
			}
			if pk, ok := col.PrimaryKey(); ok {
				cs.PK = pk
			}
			if unique, ok := col.Unique(); ok {
				cs.Unique = unique
			}
			if def, ok := col.DefaultValue(); ok {
				// Sequence defaults are auto increment plumbing
				if !strings.Contains(strings.ToLower(def), "nextval(") {
					cs.Default = def
				}
			}
			if auto, ok := col.AutoIncrement(); ok {
				cs.AutoInc = auto
			}
			cs.NotNull = cs.NotNull || cs.PK
			table.Columns = append(table.Columns, cs)
		}

		indexes, err := migrator.GetIndexes(name)
		if err != nil {
			return nil, fmt.Errorf("indexes of %s: %w", name, err)
		}
		for _, idx := range indexes {
			// Engine made key indexes are not schema shape
			if pk, ok := idx.PrimaryKey(); ok && pk {
				continue
			}
			if strings.HasPrefix(idx.Name(), "sqlite_autoindex") {
				continue
			}
			unique := false
			if u, ok := idx.Unique(); ok {
				unique = u
			}
			// Single column unique indexes mirror column uniqueness
			if unique && len(idx.Columns()) == 1 && isImplicitUnique(table, idx.Name(), idx.Columns()[0]) {
				continue
			}
			table.Indexes = append(table.Indexes, &IndexSpec{
				Name:    idx.Name(),
				Columns: idx.Columns(),
				Unique:  unique,
			})
		}
		spec.Tables = append(spec.Tables, table)
	}
	spec.sort()
	return spec, nil
}

// Whether an index is the engines spelling of column unique
// Named indexes from tags keep their own identity
func isImplicitUnique(t *TableSpec, indexName, column string) bool {
	col := t.Column(column)
	if col == nil || !col.Unique {
		return false
	}
	lower := strings.ToLower(indexName)
	return lower == strings.ToLower(t.Name)+"_"+strings.ToLower(column)+"_key" ||
		strings.HasPrefix(lower, "sqlite_autoindex")
}
