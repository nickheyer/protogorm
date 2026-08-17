package migrate

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// Engine specific behavior one backend needs
type Dialect interface {
	// Matches gorm dialector names
	Name() string
	// Folds type spellings the engine treats alike
	NormalizeType(typ string) string
	// Whether ddl rolls back inside a transaction
	TransactionalDDL() bool
	// Quotes one identifier
	Quote(ident string) string
	// Lowers logical table changes onto engine sql
	// Rebuild engines copy, alter engines change in place
	AlterPlan(t *TableChange) ([]string, error)
	// Renders create table sql for one table spec
	CreateTableSQL(t *TableSpec) (string, error)
	// Renders create index sql
	CreateIndexSQL(table string, idx *IndexSpec) string
	// Renders drop table sql
	DropTableSQL(name string) string
	// Renders rename table sql
	RenameTableSQL(from, to string) string
	// Renders drop index sql
	DropIndexSQL(table, name string) string
	// Online copy of the whole database to path
	// ErrBackupUnsupported when the engine cannot
	Backup(db *gorm.DB, path string) error
	// Engine specific consistency checks
	Check(db *gorm.DB) error
	// Session tweaks wrapping one migration run
	// Rebuild engines drop constraint enforcement here
	Begin(db *gorm.DB) error
	End(db *gorm.DB) error
}

// Engine cannot copy itself from a live connection
var ErrBackupUnsupported = fmt.Errorf("dialect cannot back up the database")

var dialects = map[string]Dialect{}

// Registers one dialect for lookups by name
func RegisterDialect(d Dialect) {
	dialects[d.Name()] = d
}

// Finds the dialect a gorm connection speaks
func DialectFor(db *gorm.DB) (Dialect, error) {
	name := db.Dialector.Name()
	if d, ok := dialects[name]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("no migrate dialect registered for %s", name)
}

// Finds one dialect by name
func DialectByName(name string) (Dialect, error) {
	if d, ok := dialects[name]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("no migrate dialect registered for %s", name)
}

// Renders one column clause shared by create paths
// Inline pk only fires for single column keys
func columnSQL(d Dialect, c *ColumnSpec, inlinePK bool, autoInc string) (string, error) {
	typ, err := c.TypeFor(d.Name())
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(d.Quote(c.Name))
	b.WriteString(" ")
	b.WriteString(typ)
	if c.PK && inlinePK {
		b.WriteString(" PRIMARY KEY")
		if c.AutoInc && autoInc != "" {
			b.WriteString(" ")
			b.WriteString(autoInc)
		}
	}
	if c.NotNull && !c.PK {
		b.WriteString(" NOT NULL")
	}
	if c.Default != "" {
		b.WriteString(" DEFAULT ")
		b.WriteString(defaultLiteral(c))
	}
	if c.Unique {
		b.WriteString(" UNIQUE")
	}
	return b.String(), nil
}

// Create table body shared by every dialect
// Auto increment spelling rides in from the dialect
func renderCreateTable(d Dialect, t *TableSpec, autoInc string) (string, error) {
	var pks []string
	for _, c := range t.Columns {
		if c.PK {
			pks = append(pks, c.Name)
		}
	}
	inlinePK := len(pks) == 1

	var parts []string
	for _, c := range t.Columns {
		clause, err := columnSQL(d, c, inlinePK, autoInc)
		if err != nil {
			return "", fmt.Errorf("table %s: %w", t.Name, err)
		}
		parts = append(parts, clause)
	}
	if len(pks) > 1 {
		quoted := make([]string, len(pks))
		for i, name := range pks {
			quoted[i] = d.Quote(name)
		}
		parts = append(parts, "PRIMARY KEY ("+strings.Join(quoted, ", ")+")")
	}
	return "CREATE TABLE " + d.Quote(t.Name) + " (" + strings.Join(parts, ", ") + ")", nil
}

// Quotes defaults that are not numbers or keywords
func defaultLiteral(c *ColumnSpec) string {
	d := strings.TrimSpace(c.Default)
	lower := strings.ToLower(d)
	switch lower {
	case "true":
		return "1"
	case "false":
		return "0"
	case "null", "current_timestamp":
		return strings.ToUpper(lower)
	}
	if isNumericLiteral(d) {
		return d
	}
	if strings.HasPrefix(d, "'") && strings.HasSuffix(d, "'") {
		return d
	}
	return "'" + strings.ReplaceAll(d, "'", "''") + "'"
}

func isNumericLiteral(s string) bool {
	if s == "" {
		return false
	}
	dot := false
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r == '-' && i == 0:
		case r == '.' && !dot:
			dot = true
		default:
			return false
		}
	}
	return true
}
