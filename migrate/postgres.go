package migrate

import (
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
)

// Postgres lowers column changes onto in place alters
type postgresDialect struct{}

func (postgresDialect) Name() string { return "postgres" }

// Postgres ddl is fully transactional
func (postgresDialect) TransactionalDDL() bool { return true }

func (postgresDialect) Quote(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

// Folds internal names onto their sql spellings
func (postgresDialect) NormalizeType(typ string) string {
	t := strings.ToLower(strings.TrimSpace(typ))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = t[:i]
	}
	t = strings.TrimSpace(t)
	switch t {
	case "int8", "bigserial", "serial8":
		return "bigint"
	case "int4", "int", "serial", "serial4":
		return "integer"
	case "int2", "smallserial", "serial2":
		return "smallint"
	case "bool":
		return "boolean"
	case "float8", "double precision":
		return "double precision"
	case "float4":
		return "real"
	case "character varying", "varchar":
		return "varchar"
	case "character", "char":
		return "char"
	case "timestamp without time zone":
		return "timestamp"
	case "timestamp with time zone":
		return "timestamptz"
	case "decimal":
		return "numeric"
	}
	return t
}

func (d postgresDialect) CreateTableSQL(t *TableSpec) (string, error) {
	// Serial types already imply their own sequence
	return renderCreateTable(d, t, "")
}

func (d postgresDialect) CreateIndexSQL(table string, idx *IndexSpec) string {
	return renderCreateIndex(d, table, idx)
}

func (d postgresDialect) DropTableSQL(name string) string {
	return "DROP TABLE IF EXISTS " + d.Quote(name)
}

func (d postgresDialect) RenameTableSQL(from, to string) string {
	return "ALTER TABLE " + d.Quote(from) + " RENAME TO " + d.Quote(to)
}

func (d postgresDialect) DropIndexSQL(_, name string) string {
	return "DROP INDEX IF EXISTS " + d.Quote(name)
}

// Alters run in place, copies ride one update statement
// Order keeps source columns alive until copies finish
func (d postgresDialect) AlterPlan(t *TableChange) ([]string, error) {
	var out []string
	table := d.Quote(t.Table.Name)
	if t.From != "" {
		out = append(out, d.RenameTableSQL(t.From, t.Table.Name))
	}

	added := map[string]bool{}
	for _, name := range t.Adds {
		col := t.Table.Column(name)
		// Backfilled columns add loose then tighten later
		render := *col
		if col.NotNull && !col.PK && t.Copy[name] != "" {
			render.NotNull = false
		}
		clause, err := columnSQL(d, &render, false, "")
		if err != nil {
			return nil, err
		}
		added[name] = true
		out = append(out, "ALTER TABLE "+table+" ADD COLUMN "+clause)
	}

	var sets []string
	for _, col := range t.Table.Columns {
		if expr, ok := t.Copy[col.Name]; ok {
			sets = append(sets, d.Quote(col.Name)+" = "+expr)
		}
	}
	if len(sets) > 0 {
		out = append(out, "UPDATE "+table+" SET "+strings.Join(sets, ", "))
	}

	for _, target := range sortedKeys(t.Renames) {
		from := t.Renames[target]
		// Copied targets already exist, just drop the source
		if _, ok := t.Copy[target]; ok {
			out = append(out, "ALTER TABLE "+table+" DROP COLUMN "+d.Quote(from))
			continue
		}
		out = append(out, "ALTER TABLE "+table+" RENAME COLUMN "+d.Quote(from)+" TO "+d.Quote(target))
	}

	for _, name := range t.Drops {
		out = append(out, "ALTER TABLE "+table+" DROP COLUMN "+d.Quote(name))
	}

	for _, name := range t.Modifies {
		col := t.Table.Column(name)
		typ, err := col.TypeFor(d.Name())
		if err != nil {
			return nil, err
		}
		quoted := d.Quote(name)
		out = append(out,
			fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::%s", table, quoted, typ, quoted, typ))
		if col.NotNull || col.PK {
			out = append(out, "ALTER TABLE "+table+" ALTER COLUMN "+quoted+" SET NOT NULL")
		} else {
			out = append(out, "ALTER TABLE "+table+" ALTER COLUMN "+quoted+" DROP NOT NULL")
		}
		if col.Default != "" {
			out = append(out, "ALTER TABLE "+table+" ALTER COLUMN "+quoted+" SET DEFAULT "+defaultLiteral(col))
		} else {
			out = append(out, "ALTER TABLE "+table+" ALTER COLUMN "+quoted+" DROP DEFAULT")
		}
	}

	// Fresh not null columns tighten after their backfill
	for _, name := range sortedKeys(added) {
		col := t.Table.Column(name)
		if col.NotNull && !col.PK && t.Copy[name] != "" {
			out = append(out, "ALTER TABLE "+table+" ALTER COLUMN "+d.Quote(name)+" SET NOT NULL")
		}
	}

	// Changed indexes ride both lists, drops run first
	for _, name := range t.DropIndexes {
		out = append(out, d.DropIndexSQL(t.Table.Name, name))
	}
	for _, name := range t.AddIndexes {
		if idx := findIndex(t.Table, name); idx != nil {
			out = append(out, d.CreateIndexSQL(t.Table.Name, idx))
		}
	}
	return out, nil
}

// Deterministic key walk over string keyed maps
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Live connections cannot copy themselves, use pg_dump
func (postgresDialect) Backup(*gorm.DB, string) error {
	return ErrBackupUnsupported
}

// Constraints stay valid through in place alters
func (postgresDialect) Check(*gorm.DB) error { return nil }

func (postgresDialect) Begin(*gorm.DB) error { return nil }

func (postgresDialect) End(*gorm.DB) error { return nil }
