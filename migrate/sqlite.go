package migrate

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// Registers builtin dialects at package load
func init() {
	RegisterDialect(sqliteDialect{})
	RegisterDialect(postgresDialect{})
}

// Sqlite lowers column changes onto full table rebuilds
type sqliteDialect struct{}

func (sqliteDialect) Name() string { return "sqlite" }

// Sqlite ddl is fully transactional
func (sqliteDialect) TransactionalDDL() bool { return true }

func (sqliteDialect) Quote(ident string) string {
	return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
}

// Folds spellings sharing one storage affinity
func (sqliteDialect) NormalizeType(typ string) string {
	t := strings.ToLower(strings.TrimSpace(typ))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = t[:i]
	}
	switch t {
	case "varchar", "char", "clob", "nvarchar", "nchar":
		return "text"
	case "int", "bigint", "smallint", "tinyint", "mediumint", "int2", "int8":
		return "integer"
	case "double", "float", "real":
		return "real"
	case "bool", "boolean":
		return "numeric"
	}
	return t
}

func (d sqliteDialect) CreateTableSQL(t *TableSpec) (string, error) {
	return renderCreateTable(d, t, "AUTOINCREMENT")
}

func (d sqliteDialect) CreateIndexSQL(table string, idx *IndexSpec) string {
	return renderCreateIndex(d, table, idx)
}

func (d sqliteDialect) DropTableSQL(name string) string {
	return "DROP TABLE IF EXISTS " + d.Quote(name)
}

func (d sqliteDialect) RenameTableSQL(from, to string) string {
	return "ALTER TABLE " + d.Quote(from) + " RENAME TO " + d.Quote(to)
}

func (d sqliteDialect) DropIndexSQL(_, name string) string {
	return "DROP INDEX IF EXISTS " + d.Quote(name)
}

// Add only changes ride plain alters, anything else rebuilds
func (d sqliteDialect) AlterPlan(t *TableChange) ([]string, error) {
	simple := len(t.Drops) == 0 && len(t.Renames) == 0 && len(t.Modifies) == 0 &&
		len(t.Copy) == 0 && t.From == ""
	if simple {
		var out []string
		for _, name := range t.Adds {
			col := t.Table.Column(name)
			clause, err := columnSQL(d, col, false, "")
			if err != nil {
				return nil, err
			}
			out = append(out, "ALTER TABLE "+d.Quote(t.Table.Name)+" ADD COLUMN "+clause)
		}
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
	return d.rebuild(t)
}

// Twelve step table rebuild copying every surviving row
func (d sqliteDialect) rebuild(t *TableChange) ([]string, error) {
	tmp := &TableSpec{Name: "_mig_" + t.Table.Name, Columns: t.Table.Columns}
	create, err := d.CreateTableSQL(tmp)
	if err != nil {
		return nil, err
	}

	var targets, exprs []string
	for _, col := range t.Table.Columns {
		expr := t.sourceExpr(d, col.Name)
		if expr == "" {
			// Fresh columns fill from their default
			if col.Default != "" {
				expr = defaultLiteral(col)
			} else {
				expr = "NULL"
			}
		}
		targets = append(targets, d.Quote(col.Name))
		exprs = append(exprs, expr)
	}

	out := []string{
		create,
		fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s",
			d.Quote(tmp.Name),
			strings.Join(targets, ", "),
			strings.Join(exprs, ", "),
			d.Quote(t.source())),
		d.DropTableSQL(t.source()),
		d.RenameTableSQL(tmp.Name, t.Table.Name),
	}
	for _, idx := range t.Table.Indexes {
		out = append(out, d.CreateIndexSQL(t.Table.Name, idx))
	}
	return out, nil
}

// Online copy through vacuum into
func (sqliteDialect) Backup(db *gorm.DB, path string) error {
	return db.Exec("VACUUM INTO ?", path).Error
}

// Runs the built in consistency checks
func (sqliteDialect) Check(db *gorm.DB) error {
	var verdict string
	if err := db.Raw("PRAGMA integrity_check").Scan(&verdict).Error; err != nil {
		return err
	}
	if verdict != "ok" {
		return fmt.Errorf("integrity check failed, %s", verdict)
	}
	var violations []struct {
		Table string `gorm:"column:table"`
		Rowid int64  `gorm:"column:rowid"`
	}
	if err := db.Raw("PRAGMA foreign_key_check").Scan(&violations).Error; err != nil {
		return err
	}
	if len(violations) > 0 {
		return fmt.Errorf("%d foreign key violations, first in table %s", len(violations), violations[0].Table)
	}
	return nil
}

// Constraint enforcement pauses while tables rebuild
// Pragma only works outside transactions on this connection
func (sqliteDialect) Begin(db *gorm.DB) error {
	return db.Exec("PRAGMA foreign_keys = OFF").Error
}

func (sqliteDialect) End(db *gorm.DB) error {
	return db.Exec("PRAGMA foreign_keys = ON").Error
}

// Shared create index renderer
func renderCreateIndex(d Dialect, table string, idx *IndexSpec) string {
	unique := ""
	if idx.Unique {
		unique = "UNIQUE "
	}
	cols := make([]string, len(idx.Columns))
	for i, c := range idx.Columns {
		cols[i] = d.Quote(c)
	}
	return fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)",
		unique, d.Quote(idx.Name), d.Quote(table), strings.Join(cols, ", "))
}
