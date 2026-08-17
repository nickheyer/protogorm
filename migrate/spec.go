// Package migrate moves live databases between released schemas
package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Whole desired schema for one model set
type Spec struct {
	Tables []*TableSpec `json:"tables"`
}

// One table with its columns and indexes
type TableSpec struct {
	Name    string        `json:"name"`
	Columns []*ColumnSpec `json:"columns"`
	Indexes []*IndexSpec  `json:"indexes,omitempty"`
}

// One column with per dialect storage types
type ColumnSpec struct {
	Name string `json:"name"`
	// Column type rendered per dialect name
	Types   map[string]string `json:"types"`
	NotNull bool              `json:"not_null,omitempty"`
	Default string            `json:"default,omitempty"`
	PK      bool              `json:"pk,omitempty"`
	AutoInc bool              `json:"auto_inc,omitempty"`
	Unique  bool              `json:"unique,omitempty"`
}

// One named index over table columns
type IndexSpec struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique,omitempty"`
}

// Inputs the desired schema derives from
type SpecSource struct {
	// Generated models, usually the AllModels slice
	Models []any
	// Every dialect the app can run on
	Dialectors []gorm.Dialector
	// Nil falls back to gorm defaults
	Namer schema.Namer
}

// Builds the desired schema from parsed gorm models
// Types come from each dialector so gorm stays the authority
func SpecOf(src SpecSource) (*Spec, error) {
	if len(src.Models) == 0 {
		return nil, fmt.Errorf("spec needs at least one model")
	}
	if len(src.Dialectors) == 0 {
		return nil, fmt.Errorf("spec needs at least one dialector")
	}
	namer := src.Namer
	if namer == nil {
		namer = schema.NamingStrategy{IdentifierMaxLength: 64}
	}
	cache := &sync.Map{}

	spec := &Spec{}
	seen := map[string]bool{}
	for _, model := range src.Models {
		parsed, err := schema.Parse(model, cache, namer)
		if err != nil {
			return nil, fmt.Errorf("parse model %T: %w", model, err)
		}
		if seen[parsed.Table] {
			return nil, fmt.Errorf("table %s declared twice", parsed.Table)
		}
		seen[parsed.Table] = true

		table := &TableSpec{Name: parsed.Table}
		for _, field := range parsed.Fields {
			if field.DBName == "" {
				continue
			}
			col := &ColumnSpec{
				Name:    field.DBName,
				Types:   map[string]string{},
				NotNull: field.NotNull || field.PrimaryKey,
				PK:      field.PrimaryKey,
				AutoInc: field.AutoIncrement,
				Unique:  field.Unique,
			}
			if field.HasDefaultValue && field.DefaultValue != "" {
				col.Default = field.DefaultValue
			}
			for _, d := range src.Dialectors {
				col.Types[d.Name()] = d.DataTypeOf(field)
			}
			table.Columns = append(table.Columns, col)
		}
		for _, idx := range parsed.ParseIndexes() {
			index := &IndexSpec{
				Name:   idx.Name,
				Unique: strings.EqualFold(idx.Class, "UNIQUE"),
			}
			for _, f := range idx.Fields {
				index.Columns = append(index.Columns, f.DBName)
			}
			table.Indexes = append(table.Indexes, index)
		}
		spec.Tables = append(spec.Tables, table)
	}
	spec.sort()
	return spec, nil
}

// Orders everything so serialization stays deterministic
// Index column order is meaning and never sorts
func (s *Spec) sort() {
	sort.Slice(s.Tables, func(i, j int) bool { return s.Tables[i].Name < s.Tables[j].Name })
	for _, t := range s.Tables {
		sort.Slice(t.Columns, func(i, j int) bool { return t.Columns[i].Name < t.Columns[j].Name })
		sort.Slice(t.Indexes, func(i, j int) bool { return t.Indexes[i].Name < t.Indexes[j].Name })
	}
}

// Finds one table by name
func (s *Spec) Table(name string) *TableSpec {
	for _, t := range s.Tables {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// Finds one column by name
func (t *TableSpec) Column(name string) *ColumnSpec {
	for _, c := range t.Columns {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Finds one index by name
func (t *TableSpec) Index(name string) *IndexSpec {
	return findIndex(t, name)
}

// Parses a snapshot or panics, for embedded files
func MustParseSpec(data []byte) *Spec {
	spec, err := ParseSpec(data)
	if err != nil {
		panic(err)
	}
	return spec
}

// Column type for one dialect
func (c *ColumnSpec) TypeFor(dialect string) (string, error) {
	if typ, ok := c.Types[dialect]; ok && typ != "" {
		return typ, nil
	}
	return "", fmt.Errorf("column %s has no type for dialect %s", c.Name, dialect)
}

// Deterministic json for committed snapshot files
func (s *Spec) MarshalCanonical() ([]byte, error) {
	clone := s.clone()
	clone.sort()
	return json.MarshalIndent(clone, "", "  ")
}

// Reads one committed snapshot file
func ParseSpec(data []byte) (*Spec, error) {
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	spec.sort()
	return &spec, nil
}

// Deep copy keeping the receiver untouched
func (s *Spec) clone() *Spec {
	out := &Spec{}
	for _, t := range s.Tables {
		nt := &TableSpec{Name: t.Name}
		for _, c := range t.Columns {
			nc := *c
			nc.Types = map[string]string{}
			for k, v := range c.Types {
				nc.Types[k] = v
			}
			nt.Columns = append(nt.Columns, &nc)
		}
		for _, idx := range t.Indexes {
			ni := *idx
			ni.Columns = append([]string(nil), idx.Columns...)
			nt.Indexes = append(nt.Indexes, &ni)
		}
		out.Tables = append(out.Tables, nt)
	}
	return out
}

// Comparable shape one dialect sees for one column
// Auto increment stays out, engines cannot all report it
type columnPrint struct {
	Name    string `json:"n"`
	Type    string `json:"t"`
	NotNull bool   `json:"nn,omitempty"`
	Default string `json:"d,omitempty"`
	PK      bool   `json:"pk,omitempty"`
	Unique  bool   `json:"u,omitempty"`
}

type indexPrint struct {
	Name    string   `json:"n"`
	Columns []string `json:"c"`
	Unique  bool     `json:"u,omitempty"`
}

type tablePrint struct {
	Name    string        `json:"n"`
	Columns []columnPrint `json:"c"`
	Indexes []indexPrint  `json:"i,omitempty"`
}

// Stable hash of the schema as one dialect stores it
// Physical column order never participates on purpose
func (s *Spec) Fingerprint(d Dialect) (string, error) {
	var tables []tablePrint
	for _, t := range s.Tables {
		tp := tablePrint{Name: t.Name}
		for _, c := range t.Columns {
			typ, err := c.TypeFor(d.Name())
			if err != nil {
				return "", fmt.Errorf("table %s: %w", t.Name, err)
			}
			tp.Columns = append(tp.Columns, columnPrint{
				Name:    c.Name,
				Type:    d.NormalizeType(typ),
				NotNull: c.NotNull || c.PK,
				Default: normalizeDefault(c.Default),
				PK:      c.PK,
				Unique:  c.Unique,
			})
		}
		sort.Slice(tp.Columns, func(i, j int) bool { return tp.Columns[i].Name < tp.Columns[j].Name })
		for _, idx := range t.Indexes {
			tp.Indexes = append(tp.Indexes, indexPrint{
				Name:    strings.ToLower(idx.Name),
				Columns: idx.Columns,
				Unique:  idx.Unique,
			})
		}
		sort.Slice(tp.Indexes, func(i, j int) bool { return tp.Indexes[i].Name < tp.Indexes[j].Name })
		tables = append(tables, tp)
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })

	raw, err := json.Marshal(tables)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Folds default literals engines quote differently
func normalizeDefault(def string) string {
	d := strings.TrimSpace(strings.ToLower(def))
	d = strings.Trim(d, "'\"")
	switch d {
	case "true":
		return "1"
	case "false":
		return "0"
	case "null":
		return ""
	}
	return d
}
