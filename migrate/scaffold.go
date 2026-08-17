package migrate

import (
	"fmt"
	"go/format"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Inputs for scaffolding the next chain migration
type ScaffoldRequest struct {
	// Package name of the migrations package
	Package string
	// Short migration name like add_lobby_flags
	Name string
	// Position the new migration takes, starting at one
	Ordinal int
	// Snapshot the chain currently ends on
	From *Spec
	// Fresh spec from the generated models
	Head *Spec
	// Author answers for destructive ambiguity
	Resolution *Resolution
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Builds the next migration file pair from a diff
// Demands come back instead of files until resolved
func Scaffold(req ScaffoldRequest) (map[string][]byte, []Demand, error) {
	if req.From == nil || req.Head == nil {
		return nil, nil, fmt.Errorf("scaffold needs a from snapshot and a head spec")
	}
	if !namePattern.MatchString(req.Name) {
		return nil, nil, fmt.Errorf("migration name %q must be lower snake case", req.Name)
	}
	if req.Package == "" {
		return nil, nil, fmt.Errorf("scaffold needs the migrations package name")
	}
	if req.Ordinal < 1 {
		return nil, nil, fmt.Errorf("scaffold ordinal %d must start at one", req.Ordinal)
	}

	ops, demands, err := Diff(req.From, req.Head, req.Resolution)
	if err != nil {
		return nil, nil, err
	}
	if len(demands) > 0 {
		return nil, demands, nil
	}
	if len(ops) == 0 {
		return nil, nil, fmt.Errorf("schema is unchanged, nothing to scaffold")
	}

	base := fmt.Sprintf("%04d_%s", req.Ordinal, req.Name)

	snapshot, err := req.Head.MarshalCanonical()
	if err != nil {
		return nil, nil, err
	}

	source, err := renderMigrationFile(req.Package, base, req.Ordinal, req.Name, ops)
	if err != nil {
		return nil, nil, err
	}

	return map[string][]byte{
		base + ".go":            source,
		base + ".snapshot.json": snapshot,
	}, nil, nil
}

// Renders the migrations package scaffolding file
// Emitted once when a migrations directory starts empty
func RenderRegistryBootstrap(pkg string) ([]byte, error) {
	src := fmt.Sprintf(`// Migration chain assembled from committed snapshots
package %s

import (
	"embed"

	"github.com/nickheyer/protogorm/migrate"
)

//go:embed *.snapshot.json
var snapshots embed.FS

// Chain every migration file registers into
var Registry = &migrate.Registry{Genesis: genesis()}

// Desired schema this build ships
func Head() *migrate.Spec {
	return mustSnapshot("head.snapshot.json")
}

// Position zero spec when a genesis file exists
func genesis() *migrate.Spec {
	data, err := snapshots.ReadFile("genesis.snapshot.json")
	if err != nil {
		return nil
	}
	return migrate.MustParseSpec(data)
}

// Reads one committed snapshot or panics
func mustSnapshot(name string) *migrate.Spec {
	data, err := snapshots.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return migrate.MustParseSpec(data)
}
`, pkg)
	return format.Source([]byte(src))
}

// Renders the registration source for one migration
func renderMigrationFile(pkg, base string, ordinal int, name string, ops []Op) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, `// Scaffolded by protogorm migrate, ops are yours to edit
package %s

import "github.com/nickheyer/protogorm/migrate"

func init() {
	target := mustSnapshot(%q)
	Registry.MustAdd(&migrate.Migration{
		Ordinal: %d,
		Name:    %q,
		Target:  target,
		Ops: []migrate.Op{
`, pkg, base+".snapshot.json", ordinal, name)

	for _, op := range ops {
		rendered, err := renderOp(op)
		if err != nil {
			return nil, err
		}
		b.WriteString("\t\t\t")
		b.WriteString(rendered)
		b.WriteString(",\n")
	}
	b.WriteString("\t\t},\n\t})\n}\n")

	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return nil, fmt.Errorf("format scaffold: %w\n%s", err, b.String())
	}
	return formatted, nil
}

// Renders one op as go source against the target spec
func renderOp(op Op) (string, error) {
	switch o := op.(type) {
	case CreateTable:
		return fmt.Sprintf("migrate.CreateTable{Table: target.Table(%q)}", o.Table.Name), nil
	case DropTable:
		return fmt.Sprintf("migrate.DropTable{Name: %q}", o.Name), nil
	case RenameTable:
		return fmt.Sprintf("migrate.RenameTable{From: %q, To: %q}", o.From, o.To), nil
	case CreateIndex:
		return fmt.Sprintf("migrate.CreateIndex{Table: %q, Index: target.Table(%q).Index(%q)}",
			o.Table, o.Table, o.Index.Name), nil
	case DropIndex:
		return fmt.Sprintf("migrate.DropIndex{Table: %q, Name: %q}", o.Table, o.Name), nil
	case TableChange:
		return renderTableChange(o)
	case Exec, Transform:
		return "", fmt.Errorf("op %T never scaffolds, add it by hand", op)
	}
	return "", fmt.Errorf("unknown op %T", op)
}

func renderTableChange(o TableChange) (string, error) {
	var parts []string
	parts = append(parts, fmt.Sprintf("Table: target.Table(%q)", o.Table.Name))
	if o.From != "" {
		parts = append(parts, fmt.Sprintf("From: %q", o.From))
	}
	if len(o.Adds) > 0 {
		parts = append(parts, "Adds: "+renderStrings(o.Adds))
	}
	if len(o.Drops) > 0 {
		parts = append(parts, "Drops: "+renderStrings(o.Drops))
	}
	if len(o.Renames) > 0 {
		parts = append(parts, "Renames: "+renderStringMap(o.Renames))
	}
	if len(o.Modifies) > 0 {
		parts = append(parts, "Modifies: "+renderStrings(o.Modifies))
	}
	if len(o.Copy) > 0 {
		parts = append(parts, "Copy: "+renderStringMap(o.Copy))
	}
	if len(o.AddIndexes) > 0 {
		parts = append(parts, "AddIndexes: "+renderStrings(o.AddIndexes))
	}
	if len(o.DropIndexes) > 0 {
		parts = append(parts, "DropIndexes: "+renderStrings(o.DropIndexes))
	}
	return "migrate.TableChange{" + strings.Join(parts, ", ") + "}", nil
}

func renderStrings(list []string) string {
	quoted := make([]string, len(list))
	for i, s := range list {
		quoted[i] = strconv.Quote(s)
	}
	return "[]string{" + strings.Join(quoted, ", ") + "}"
}

func renderStringMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = strconv.Quote(k) + ": " + strconv.Quote(m[k])
	}
	return "map[string]string{" + strings.Join(parts, ", ") + "}"
}
