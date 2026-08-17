// Derives the migrate schema spec straight from descriptors
package generator

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/nickheyer/protogorm/migrate"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// One column claiming a place inside a shared index
type indexEntry struct {
	column   string
	priority int
}

// One index accumulating columns across fields
type pendingIndex struct {
	name    string
	unique  bool
	where   string
	entries []indexEntry
}

// Builds the desired schema from collected models
// Types render through every dialect, protogorm owns ddl
func BuildSpec(models []*Model, dialects []migrate.Dialect) (*migrate.Spec, error) {
	if len(models) == 0 {
		return nil, fmt.Errorf("spec needs at least one model")
	}
	if len(dialects) == 0 {
		return nil, fmt.Errorf("spec needs at least one dialect")
	}
	spec := &migrate.Spec{}
	seen := map[string]string{}
	for _, m := range models {
		if prev, ok := seen[m.Opts.Table]; ok {
			return nil, fmt.Errorf("table %s declared by both %s and %s", m.Opts.Table, prev, m.Name)
		}
		seen[m.Opts.Table] = m.Name
		table, err := buildTable(m, dialects)
		if err != nil {
			return nil, err
		}
		spec.Tables = append(spec.Tables, table)
	}
	return spec, nil
}

// Builds one table spec with columns and indexes
func buildTable(m *Model, dialects []migrate.Dialect) (*migrate.TableSpec, error) {
	table := &migrate.TableSpec{
		Name: m.Opts.Table,
		Was:  append([]string(nil), m.Opts.Was...),
	}
	reserved := m.Msg.ReservedNames()
	for i := 0; i < reserved.Len(); i++ {
		table.Reserved = append(table.Reserved, string(reserved.Get(i)))
	}

	indexes := map[string]*pendingIndex{}
	var indexOrder []string

	for _, fs := range m.Fields {
		if fs.Skip || fs.Relation {
			continue
		}
		col, err := buildColumn(m, fs, dialects)
		if err != nil {
			return nil, err
		}
		table.Columns = append(table.Columns, col)

		for _, s := range fs.Settings {
			if s.Key != "INDEX" && s.Key != "UNIQUEINDEX" {
				continue
			}
			name, priority, where, err := parseIndexSetting(s.Value)
			if err != nil {
				return nil, fmt.Errorf("model %s column %s: %w", m.Name, fs.Column, err)
			}
			if name == "" {
				name = "idx_" + m.Opts.Table + "_" + fs.Column
			}
			idx, ok := indexes[name]
			if !ok {
				idx = &pendingIndex{name: name}
				indexes[name] = idx
				indexOrder = append(indexOrder, name)
			}
			if s.Key == "UNIQUEINDEX" {
				idx.unique = true
			}
			if where != "" {
				if idx.where != "" && idx.where != where {
					return nil, fmt.Errorf("index %s declares two where clauses", name)
				}
				idx.where = where
			}
			idx.entries = append(idx.entries, indexEntry{
				column:   fs.Column,
				priority: priority,
			})
		}
	}

	for _, name := range indexOrder {
		idx := indexes[name]
		sort.SliceStable(idx.entries, func(i, j int) bool {
			return idx.entries[i].priority < idx.entries[j].priority
		})
		out := &migrate.IndexSpec{Name: name, Unique: idx.unique, Where: idx.where}
		for _, e := range idx.entries {
			out.Columns = append(out.Columns, e.column)
		}
		table.Indexes = append(table.Indexes, out)
	}
	return table, nil
}

// Builds one column spec rendering types per dialect
func buildColumn(m *Model, fs *Field, dialects []migrate.Dialect) (*migrate.ColumnSpec, error) {
	col := &migrate.ColumnSpec{
		Name:    fs.Column,
		Types:   map[string]string{},
		PK:      fs.PK,
		AutoInc: fs.AutoInc,
		Was:     append([]string(nil), fs.Was...),
	}
	logical := migrate.LogicalColumn{Kind: kindOf(fs.Desc), AutoInc: fs.AutoInc, PK: fs.PK}
	override := ""
	for _, s := range fs.Settings {
		switch s.Key {
		case "NOT NULL", "NOTNULL":
			col.NotNull = true
		case "DEFAULT":
			col.Default = s.Value
		case "UNIQUE":
			col.Unique = true
		case "TYPE":
			override = s.Value
		case "SIZE":
			n, err := strconv.Atoi(s.Value)
			if err != nil {
				return nil, fmt.Errorf("model %s column %s bad size %q", m.Name, fs.Column, s.Value)
			}
			logical.Size = n
		case "PRECISION":
			n, err := strconv.Atoi(s.Value)
			if err != nil {
				return nil, fmt.Errorf("model %s column %s bad precision %q", m.Name, fs.Column, s.Value)
			}
			logical.Precision = n
		case "SCALE":
			n, err := strconv.Atoi(s.Value)
			if err != nil {
				return nil, fmt.Errorf("model %s column %s bad scale %q", m.Name, fs.Column, s.Value)
			}
			logical.Scale = n
		case "CHECK", "EMBEDDED", "EMBEDDEDPREFIX":
			return nil, fmt.Errorf("model %s column %s tag %s has no spec support", m.Name, fs.Column, s.Key)
		}
	}
	col.NotNull = col.NotNull || col.PK
	for _, d := range dialects {
		if override != "" {
			col.Types[d.Name()] = override
			continue
		}
		col.Types[d.Name()] = d.TypeOf(logical)
	}
	return col, nil
}

// Logical storage class one descriptor field lands in
func kindOf(fd protoreflect.FieldDescriptor) migrate.LogicalType {
	if fd.IsMap() || fd.IsList() {
		return migrate.TypeJSON
	}
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		if fd.Message().FullName() == "google.protobuf.Timestamp" {
			return migrate.TypeTime
		}
		return migrate.TypeJSON
	case protoreflect.EnumKind:
		return migrate.TypeEnum
	case protoreflect.BoolKind:
		return migrate.TypeBool
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return migrate.TypeInt32
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return migrate.TypeInt64
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return migrate.TypeUint32
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return migrate.TypeUint64
	case protoreflect.FloatKind:
		return migrate.TypeFloat
	case protoreflect.DoubleKind:
		return migrate.TypeDouble
	case protoreflect.BytesKind:
		return migrate.TypeBytes
	}
	return migrate.TypeText
}

// Splits one index tag value into its supported options
// First comma part without a colon names the index
func parseIndexSetting(value string) (string, int, string, error) {
	name := ""
	priority := 10
	where := ""
	parts := strings.Split(value, ",")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i == 0 && !strings.Contains(part, ":") {
			name = part
			continue
		}
		key, val, _ := strings.Cut(part, ":")
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "priority":
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil {
				return "", 0, "", fmt.Errorf("bad index priority %q", val)
			}
			priority = n
		case "where":
			where = strings.TrimSpace(val)
		case "unique":
			// Unique already rides the uniqueIndex key
		default:
			return "", 0, "", fmt.Errorf("index option %q has no spec support", part)
		}
	}
	return name, priority, where, nil
}
