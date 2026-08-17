// Rewrites generated pb.go struct tags from model annotations
package generator

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// One field's computed struct tags
type fieldTags struct {
	gorm   string
	redact bool
}

// Walks the directory and injects tags into model structs
func InjectDir(dir string, models []*Model) (int, error) {
	tagged := map[string]map[string]fieldTags{}
	for _, m := range models {
		tagged[m.Name] = computeTags(m)
	}
	injected := 0
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".pb.go") {
			return err
		}
		n, err := injectFile(path, tagged)
		injected += n
		return err
	})
	return injected, err
}

// Builds the tag set for one model
func computeTags(m *Model) map[string]fieldTags {
	tags := map[string]fieldTags{}
	for _, fs := range m.Fields {
		ft := fieldTags{redact: fs.Redact}
		switch {
		case fs.Skip:
			ft.gorm = "-"
		case fs.Relation:
			frags := []string{}
			if !strings.Contains(fs.Tag, "foreignKey:") {
				frags = append(frags, "foreignKey:"+fs.GoName+"Id")
			}
			if fs.Tag != "" {
				frags = append(frags, fs.Tag)
			}
			ft.gorm = strings.Join(frags, ";")
		default:
			frags := []string{"column:" + fs.Column}
			frags = append(frags, autoSerializer(fs.Desc)...)
			for _, part := range strings.Split(fs.Tag, ";") {
				if part == "" || strings.HasPrefix(part, "column:") {
					continue
				}
				frags = append(frags, part)
			}
			ft.gorm = strings.Join(frags, ";")
		}
		tags[fs.GoName] = ft
	}
	return tags
}

// Picks storage serializers from the field shape
// Column types stay out, the migrate spec owns ddl
func autoSerializer(fd protoreflect.FieldDescriptor) []string {
	if fd.IsMap() || fd.IsList() {
		return []string{"serializer:json"}
	}
	if fd.Kind() == protoreflect.MessageKind {
		if fd.Message().FullName() == "google.protobuf.Timestamp" {
			return []string{"serializer:tspb"}
		}
		return []string{"serializer:json"}
	}
	return nil
}

// Rewrites struct tags in one generated file
func injectFile(path string, tagged map[string]map[string]fieldTags) (int, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return 0, err
	}

	n := 0
	changed := false
	ast.Inspect(f, func(node ast.Node) bool {
		ts, ok := node.(*ast.TypeSpec)
		if !ok {
			return true
		}
		ft, ok := tagged[ts.Name.Name]
		if !ok {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		n++
		for _, fld := range st.Fields.List {
			if len(fld.Names) != 1 || fld.Tag == nil {
				continue
			}
			spec, ok := ft[fld.Names[0].Name]
			if !ok {
				continue
			}
			raw := stripTagKey(strings.Trim(fld.Tag.Value, "`"), "gorm")
			if spec.redact {
				raw = stripTagKey(raw, "json") + ` json:"-"`
			}
			fld.Tag.Value = "`" + strings.TrimSpace(raw) + ` gorm:"` + spec.gorm + `"` + "`"
			changed = true
		}
		return true
	})
	if !changed {
		return n, nil
	}

	var buf bytes.Buffer
	if err := (&printer.Config{Mode: printer.TabIndent, Tabwidth: 8}).Fprint(&buf, fset, f); err != nil {
		return n, err
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return n, err
	}
	return n, os.WriteFile(path, formatted, 0644)
}

// Removes one key from a struct tag string
func stripTagKey(raw, key string) string {
	var kept []string
	for _, part := range splitTag(raw) {
		if !strings.HasPrefix(part, key+`:"`) {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " ")
}

// Splits a struct tag into key value units
func splitTag(raw string) []string {
	var parts []string
	for raw = strings.TrimSpace(raw); raw != ""; raw = strings.TrimSpace(raw) {
		colon := strings.Index(raw, `:"`)
		if colon < 0 {
			parts = append(parts, raw)
			break
		}
		end := strings.Index(raw[colon+2:], `"`)
		if end < 0 {
			parts = append(parts, raw)
			break
		}
		parts = append(parts, raw[:colon+2+end+1])
		raw = raw[colon+2+end+1:]
	}
	return parts
}
