package migrate

import (
	"fmt"
	"slices"
)

// One authoring decision the diff refuses to make alone
type Demand struct {
	Table  string
	Column string
	Kind   string
	Detail string
}

func (d Demand) String() string {
	where := d.Table
	if d.Column != "" {
		where += "." + d.Column
	}
	return fmt.Sprintf("%s %s, %s", d.Kind, where, d.Detail)
}

// Author answers resolving every destructive ambiguity
// Renames and drops declared in the proto need no entry
type Resolution struct {
	// Source table keyed by target table name
	TableRenames map[string]string `json:"table_renames,omitempty"`
	// Source column keyed by table then target column
	Renames map[string]map[string]string `json:"renames,omitempty"`
	// Sql expressions keyed by table then target column
	Copy map[string]map[string]string `json:"copy,omitempty"`
	// Tables the author confirms losing
	DropTables []string `json:"drop_tables,omitempty"`
	// Columns the author confirms losing per table
	DropColumns map[string][]string `json:"drop_columns,omitempty"`
	// Changed columns the author confirms rewriting in place
	ConfirmModify map[string][]string `json:"confirm_modify,omitempty"`
}

func (r *Resolution) renameFor(table, target string) (string, bool) {
	if r == nil {
		return "", false
	}
	from, ok := r.Renames[table][target]
	return from, ok
}

func (r *Resolution) copyFor(table, target string) (string, bool) {
	if r == nil {
		return "", false
	}
	expr, ok := r.Copy[table][target]
	return expr, ok
}

func (r *Resolution) dropsColumn(table, column string) bool {
	return r != nil && slices.Contains(r.DropColumns[table], column)
}

func (r *Resolution) dropsTable(table string) bool {
	return r != nil && slices.Contains(r.DropTables, table)
}

func (r *Resolution) confirmsModify(table, column string) bool {
	return r != nil && slices.Contains(r.ConfirmModify[table], column)
}

func (r *Resolution) tableSource(target string) (string, bool) {
	if r == nil {
		return "", false
	}
	from, ok := r.TableRenames[target]
	return from, ok
}

// Former name carried by the target and alive in from
func wasSource(from, to *Spec, target *TableSpec, claimed map[string]bool) (string, bool) {
	for _, was := range target.Was {
		if claimed[was] || to.Table(was) != nil {
			continue
		}
		if from.Table(was) != nil {
			return was, true
		}
	}
	return "", false
}

// Former column name alive in source and free in target
func wasColumn(source, target *TableSpec, col *ColumnSpec, claimed map[string]bool) (string, bool) {
	for _, was := range col.Was {
		if claimed[was] || target.Column(was) != nil {
			continue
		}
		if source.Column(was) != nil {
			return was, true
		}
	}
	return "", false
}

// Structural changes landing from onto to
// Unresolved ambiguity comes back as demands, never ops
func Diff(from, to *Spec, res *Resolution) ([]Op, []Demand, error) {
	var ops []Op
	var demands []Demand
	claimed := map[string]bool{}

	for _, target := range to.Tables {
		var source *TableSpec
		sourceName := target.Name
		if renamed, ok := res.tableSource(target.Name); ok {
			sourceName = renamed
		} else if from.Table(sourceName) == nil {
			if was, ok := wasSource(from, to, target, claimed); ok {
				sourceName = was
			}
		}
		source = from.Table(sourceName)
		if source != nil {
			claimed[sourceName] = true
			change, tableDemands, err := diffTable(source, target, sourceName != target.Name, res)
			if err != nil {
				return nil, nil, err
			}
			demands = append(demands, tableDemands...)
			if change != nil {
				ops = append(ops, *change)
			}
			continue
		}
		ops = append(ops, CreateTable{Table: target})
	}

	for _, source := range from.Tables {
		if claimed[source.Name] || to.Table(source.Name) != nil {
			continue
		}
		if res.dropsTable(source.Name) {
			ops = append(ops, DropTable{Name: source.Name})
			continue
		}
		demands = append(demands, Demand{
			Table:  source.Name,
			Kind:   "vanishing_table",
			Detail: "confirm the drop or add a was name on its replacement",
		})
	}
	return ops, demands, nil
}

// Column level changes for one surviving table
// Nil change means the columns already match
func diffTable(source, target *TableSpec, renamed bool, res *Resolution) (*TableChange, []Demand, error) {
	change := &TableChange{
		Table:   target,
		Renames: map[string]string{},
		Copy:    map[string]string{},
	}
	if renamed {
		change.From = source.Name
	}
	var demands []Demand
	claimed := map[string]bool{}

	for _, col := range target.Columns {
		if expr, ok := res.copyFor(target.Name, col.Name); ok {
			change.Copy[col.Name] = expr
		}
		if from, ok := res.renameFor(target.Name, col.Name); ok {
			if source.Column(from) == nil {
				return nil, nil, fmt.Errorf("table %s renames missing column %s", target.Name, from)
			}
			claimed[from] = true
			change.Renames[col.Name] = from
			continue
		}
		if source.Column(col.Name) == nil {
			if was, ok := wasColumn(source, target, col, claimed); ok {
				claimed[was] = true
				change.Renames[col.Name] = was
				continue
			}
		}
		old := source.Column(col.Name)
		if old == nil {
			change.Adds = append(change.Adds, col.Name)
			if col.NotNull && !col.PK && col.Default == "" && change.Copy[col.Name] == "" {
				demands = append(demands, Demand{
					Table:  target.Name,
					Column: col.Name,
					Kind:   "new_not_null",
					Detail: "give it a default or a copy expression",
				})
			}
			continue
		}
		if !sameColumn(old, col) {
			change.Modifies = append(change.Modifies, col.Name)
			if !res.confirmsModify(target.Name, col.Name) && change.Copy[col.Name] == "" {
				demands = append(demands, Demand{
					Table:  target.Name,
					Column: col.Name,
					Kind:   "changed_column",
					Detail: "confirm the in place rewrite or give a copy expression",
				})
			}
		}
	}

	for _, old := range source.Columns {
		if claimed[old.Name] || target.Column(old.Name) != nil {
			continue
		}
		if res.dropsColumn(target.Name, old.Name) || slices.Contains(target.Reserved, old.Name) {
			change.Drops = append(change.Drops, old.Name)
			continue
		}
		demands = append(demands, Demand{
			Table:  target.Name,
			Column: old.Name,
			Kind:   "vanishing_column",
			Detail: "confirm the drop, reserve the field name, or name its replacement",
		})
	}

	// Changed indexes land in both lists, drops run first
	for _, idx := range target.Indexes {
		old := findIndex(source, idx.Name)
		if old == nil {
			change.AddIndexes = append(change.AddIndexes, idx.Name)
			continue
		}
		if old.Unique != idx.Unique || !slices.Equal(old.Columns, idx.Columns) || old.Where != idx.Where {
			change.DropIndexes = append(change.DropIndexes, idx.Name)
			change.AddIndexes = append(change.AddIndexes, idx.Name)
		}
	}
	for _, idx := range source.Indexes {
		if findIndex(target, idx.Name) == nil {
			change.DropIndexes = append(change.DropIndexes, idx.Name)
		}
	}

	unchanged := !renamed && len(change.Adds) == 0 && len(change.Drops) == 0 &&
		len(change.Renames) == 0 && len(change.Modifies) == 0 && len(change.Copy) == 0 &&
		len(change.AddIndexes) == 0 && len(change.DropIndexes) == 0
	if unchanged {
		return nil, demands, nil
	}
	return change, demands, nil
}

// Whether two column definitions store identically anywhere
func sameColumn(a, b *ColumnSpec) bool {
	if a.NotNull != b.NotNull || a.PK != b.PK || a.Unique != b.Unique {
		return false
	}
	if normalizeDefault(a.Default) != normalizeDefault(b.Default) {
		return false
	}
	for name, typ := range b.Types {
		other, ok := a.Types[name]
		if !ok {
			continue
		}
		d, err := DialectByName(name)
		if err != nil {
			continue
		}
		if d.NormalizeType(typ) != d.NormalizeType(other) {
			return false
		}
	}
	return true
}

func findIndex(t *TableSpec, name string) *IndexSpec {
	for _, idx := range t.Indexes {
		if idx.Name == name {
			return idx
		}
	}
	return nil
}
