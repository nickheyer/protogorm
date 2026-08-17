package migrate

import (
	"fmt"

	"gorm.io/gorm"
)

// One logical schema or data change
// Dialects lower each op onto their own sql
type Op interface{ isOp() }

// Makes one table from its full spec
type CreateTable struct {
	Table *TableSpec
}

// Removes one table and its data
type DropTable struct {
	Name string
}

// Renames one table keeping its data
type RenameTable struct {
	From string
	To   string
}

// Adds one named index
type CreateIndex struct {
	Table string
	Index *IndexSpec
}

// Drops one named index
type DropIndex struct {
	Table string
	Name  string
}

// Batched column changes landing one table on a target shape
// Rebuild engines copy rows once, alter engines change in place
type TableChange struct {
	// Full target shape after the change
	Table *TableSpec
	// Source table name when it differs from target
	From string
	// Columns that are new in the target
	Adds []string
	// Source columns the target drops
	Drops []string
	// Source column names keyed by target column name
	Renames map[string]string
	// Target columns whose definition changed in place
	Modifies []string
	// Sql filling target columns from the old row
	// Keyed by target column, wins over renames
	Copy map[string]string
	// Target indexes that are new or changed
	AddIndexes []string
	// Source indexes the target no longer has
	DropIndexes []string
}

// Raw sql escape hatch, one statement per entry
type Exec struct {
	SQL []string
}

// Go data rewrite running inside the migration
// Touches tables by name only, never live models
type Transform struct {
	Name string
	Fn   func(tx *gorm.DB, d Dialect) error
}

func (CreateTable) isOp() {}
func (DropTable) isOp()   {}
func (RenameTable) isOp() {}
func (CreateIndex) isOp() {}
func (DropIndex) isOp()   {}
func (TableChange) isOp() {}
func (Exec) isOp()        {}
func (Transform) isOp()   {}

// Source table name for one change
func (t *TableChange) source() string {
	if t.From != "" {
		return t.From
	}
	return t.Table.Name
}

// Sql expression reading one target column from the source row
// Empty means the column is new with no source
func (t *TableChange) sourceExpr(d Dialect, target string) string {
	if expr, ok := t.Copy[target]; ok {
		return expr
	}
	for _, added := range t.Adds {
		if added == target {
			return ""
		}
	}
	if from, ok := t.Renames[target]; ok {
		return d.Quote(from)
	}
	return d.Quote(target)
}

// Guards one change against silent data loss
// Also proves every touched column types on this dialect
func (t *TableChange) validate(d Dialect) error {
	if t.Table == nil {
		return fmt.Errorf("table change needs a target spec")
	}
	for _, name := range t.Adds {
		col := t.Table.Column(name)
		if col == nil {
			return fmt.Errorf("table %s adds unknown column %s", t.Table.Name, name)
		}
		if col.NotNull && !col.PK && col.Default == "" && t.Copy[name] == "" {
			return fmt.Errorf("table %s column %s needs a default or copy expression", t.Table.Name, name)
		}
	}
	for target := range t.Renames {
		if t.Table.Column(target) == nil {
			return fmt.Errorf("table %s renames onto unknown column %s", t.Table.Name, target)
		}
	}
	for target := range t.Copy {
		if t.Table.Column(target) == nil {
			return fmt.Errorf("table %s copies onto unknown column %s", t.Table.Name, target)
		}
	}
	for _, name := range t.Modifies {
		if t.Table.Column(name) == nil {
			return fmt.Errorf("table %s modifies unknown column %s", t.Table.Name, name)
		}
	}
	for _, col := range t.Table.Columns {
		if _, err := col.TypeFor(d.Name()); err != nil {
			return fmt.Errorf("table %s: %w", t.Table.Name, err)
		}
	}
	return nil
}

// Lowers one op onto statements for one dialect
// Transforms come back whole for the engine to run
func lowerOp(d Dialect, op Op) ([]string, *Transform, error) {
	switch o := op.(type) {
	case CreateTable:
		sql, err := d.CreateTableSQL(o.Table)
		if err != nil {
			return nil, nil, err
		}
		out := []string{sql}
		for _, idx := range o.Table.Indexes {
			out = append(out, d.CreateIndexSQL(o.Table.Name, idx))
		}
		return out, nil, nil
	case DropTable:
		return []string{d.DropTableSQL(o.Name)}, nil, nil
	case RenameTable:
		return []string{d.RenameTableSQL(o.From, o.To)}, nil, nil
	case CreateIndex:
		return []string{d.CreateIndexSQL(o.Table, o.Index)}, nil, nil
	case DropIndex:
		return []string{d.DropIndexSQL(o.Table, o.Name)}, nil, nil
	case TableChange:
		if err := o.validate(d); err != nil {
			return nil, nil, err
		}
		sql, err := d.AlterPlan(&o)
		return sql, nil, err
	case Exec:
		return o.SQL, nil, nil
	case Transform:
		if o.Fn == nil {
			return nil, nil, fmt.Errorf("transform %s has no function", o.Name)
		}
		return nil, &o, nil
	}
	return nil, nil, fmt.Errorf("unknown op %T", op)
}
