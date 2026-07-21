// Builds model specs from annotated descriptors
package generator

import (
	"fmt"
	"iter"
	"sort"
	"strings"

	protogormv1 "github.com/nickheyer/protogorm/gen/protogorm/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// One go package holding generated models
type Package struct {
	ImportPath string
	Name       string
	Dir        string
}

// One persisted column or relation
type Field struct {
	GoName     string
	Column     string
	Kind       protoreflect.Kind
	Desc       protoreflect.FieldDescriptor
	Optional   bool
	Skip       bool
	Redact     bool
	Relation   bool
	AutoCreate bool
	AutoUpdate bool
	PK         bool
	AutoInc    bool
	Tag        string
}

// One table backed message
type Model struct {
	Name    string
	Pkg     *Package
	Msg     protoreflect.MessageDescriptor
	Opts    *protogormv1.Model
	Fields  []*Field
	PKs     []*Field
	Created string
	Updated string
	Redacts []string
}

// Finds every annotated message across the file set
func Collect(files iter.Seq[protoreflect.FileDescriptor]) ([]*Model, error) {
	isModel := map[protoreflect.FullName]bool{}
	type rawModel struct {
		md protoreflect.MessageDescriptor
		fd protoreflect.FileDescriptor
	}
	var raw []rawModel
	for fd := range files {
		for i := 0; i < fd.Messages().Len(); i++ {
			md := fd.Messages().Get(i)
			if modelOpts(md) != nil {
				isModel[md.FullName()] = true
				raw = append(raw, rawModel{md, fd})
			}
		}
	}

	pkgs := map[string]*Package{}
	seen := map[string]*Model{}
	var models []*Model
	for _, r := range raw {
		pkg, err := packageOf(r.fd)
		if err != nil {
			return nil, err
		}
		if have, ok := pkgs[pkg.ImportPath]; ok {
			pkg = have
		} else {
			pkgs[pkg.ImportPath] = pkg
		}

		m := &Model{Name: goCamel(string(r.md.Name())), Pkg: pkg, Msg: r.md, Opts: modelOpts(r.md)}
		if prev, ok := seen[m.Name]; ok {
			return nil, fmt.Errorf("model %s defined in both %s and %s", m.Name, prev.Msg.FullName(), r.md.FullName())
		}
		seen[m.Name] = m
		for i := 0; i < r.md.Fields().Len(); i++ {
			fs := buildField(r.md.Fields().Get(i), isModel)
			m.Fields = append(m.Fields, fs)
			if fs.PK {
				m.PKs = append(m.PKs, fs)
			}
			if fs.AutoCreate {
				m.Created = fs.GoName
			}
			if fs.AutoUpdate {
				m.Updated = fs.GoName
			}
			if fs.Redact {
				m.Redacts = append(m.Redacts, fs.GoName)
			}
		}
		if len(m.PKs) == 0 {
			return nil, fmt.Errorf("model %s has no primary key", m.Name)
		}
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Opts.Table < models[j].Opts.Table })
	return models, nil
}

// Resolves go package identity from file options
func packageOf(fd protoreflect.FileDescriptor) (*Package, error) {
	gopkg := fd.Options().(*descriptorpb.FileOptions).GetGoPackage()
	if gopkg == "" {
		return nil, fmt.Errorf("file %s has models but no go_package", fd.Path())
	}
	path, name, ok := strings.Cut(gopkg, ";")
	if !ok {
		name = path[strings.LastIndex(path, "/")+1:]
	}
	dir := "."
	if i := strings.LastIndex(fd.Path(), "/"); i >= 0 {
		dir = fd.Path()[:i]
	}
	return &Package{ImportPath: path, Name: name, Dir: dir}, nil
}

// Resolves one field's storage shape
func buildField(fd protoreflect.FieldDescriptor, isModel map[protoreflect.FullName]bool) *Field {
	fs := &Field{
		GoName:   goCamel(string(fd.Name())),
		Column:   string(fd.Name()),
		Kind:     fd.Kind(),
		Desc:     fd,
		Optional: fd.HasOptionalKeyword(),
	}
	ext := fieldOpts(fd)
	if ext != nil {
		fs.Skip = ext.Skip
		fs.Redact = ext.Redact
		fs.Tag = ext.Tag
	}
	if fd.Kind() == protoreflect.MessageKind && !fd.IsMap() && !fd.IsList() && isModel[fd.Message().FullName()] {
		fs.Relation = true
		return fs
	}
	if ext != nil {
		for _, part := range strings.Split(ext.Tag, ";") {
			switch {
			case strings.HasPrefix(part, "column:"):
				fs.Column = strings.TrimPrefix(part, "column:")
			case part == "autoCreateTime":
				fs.AutoCreate = true
			case part == "autoUpdateTime":
				fs.AutoUpdate = true
			}
		}
		if strings.Contains(ext.Tag, "primaryKey") {
			fs.PK = true
		}
		if strings.Contains(ext.Tag, "autoIncrement") {
			fs.AutoInc = true
		}
	}
	return fs
}

// Groups models by go package preserving collect order
func ByPackage(models []*Model) []*Package {
	var order []*Package
	seen := map[*Package]bool{}
	for _, m := range models {
		if !seen[m.Pkg] {
			seen[m.Pkg] = true
			order = append(order, m.Pkg)
		}
	}
	return order
}

func modelOpts(md protoreflect.MessageDescriptor) *protogormv1.Model {
	opts := md.Options().(*descriptorpb.MessageOptions)
	return proto.GetExtension(opts, protogormv1.E_Model).(*protogormv1.Model)
}

func fieldOpts(fd protoreflect.FieldDescriptor) *protogormv1.Field {
	opts := fd.Options().(*descriptorpb.FieldOptions)
	return proto.GetExtension(opts, protogormv1.E_Db).(*protogormv1.Field)
}

// Mirrors protoc-gen-go camel casing so injected names always match
func goCamel(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' && i == 0:
			b = append(b, 'X')
		case c == '_' && i+1 < len(s) && isLower(s[i+1]):
			// Dropped before a lowercase letter
		case c >= '0' && c <= '9':
			b = append(b, c)
		default:
			if isLower(c) {
				c -= 'a' - 'A'
			}
			b = append(b, c)
			for ; i+1 < len(s) && isLower(s[i+1]); i++ {
				b = append(b, s[i+1])
			}
		}
	}
	return string(b)
}

func isLower(c byte) bool {
	return c >= 'a' && c <= 'z'
}

// Lower spaced words from a camel name
func humanName(name string) string {
	var b strings.Builder
	for i, r := range name {
		if r >= 'A' && r <= 'Z' && i > 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// Trailing s added unless already present
func plural(name string) string {
	if strings.HasSuffix(name, "s") {
		return name
	}
	return name + "s"
}

// Lower camel go identifier from a column name
func paramName(col string) string {
	c := goCamel(col)
	if strings.HasSuffix(c, "Id") {
		c = strings.TrimSuffix(c, "Id") + "ID"
	}
	if c == "" {
		return c
	}
	r := []rune(c)
	if r[0] >= 'A' && r[0] <= 'Z' {
		r[0] += 'a' - 'A'
	}
	out := string(r)
	if out == "iD" {
		out = "id"
	}
	return out
}
