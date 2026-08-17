package migrate_test

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/nickheyer/protogorm/migrate"
	"gorm.io/gorm"
)

func sqliteDialect(t *testing.T) migrate.Dialect {
	t.Helper()
	d, err := migrate.DialectByName("sqlite")
	if err != nil {
		t.Fatalf("dialect: %v", err)
	}
	return d
}

func TestFingerprintIgnoresPhysicalOrder(t *testing.T) {
	spec := v1Spec(t)
	d := sqliteDialect(t)
	want, err := spec.Fingerprint(d)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}

	// Shuffled clone must hash identically
	data, err := spec.MarshalCanonical()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	shuffled, err := migrate.ParseSpec(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, table := range shuffled.Tables {
		for i, j := 0, len(table.Columns)-1; i < j; i, j = i+1, j-1 {
			table.Columns[i], table.Columns[j] = table.Columns[j], table.Columns[i]
		}
	}
	got, err := shuffled.Fingerprint(d)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if got != want {
		t.Fatal("column order changed the fingerprint")
	}
}

func TestFingerprintSeesRealChanges(t *testing.T) {
	d := sqliteDialect(t)
	a, err := v1Spec(t).Fingerprint(d)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	b, err := v2Spec(t).Fingerprint(d)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if a == b {
		t.Fatal("different schemas hashed alike")
	}
}

func TestFingerprintIgnoresHistory(t *testing.T) {
	spec := v2Spec(t)
	d := sqliteDialect(t)
	want, err := spec.Fingerprint(d)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}

	// Stripped history must hash identically
	data, err := spec.MarshalCanonical()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	bare, err := migrate.ParseSpec(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, table := range bare.Tables {
		table.Was = nil
		table.Reserved = nil
		for _, col := range table.Columns {
			col.Was = nil
		}
	}
	got, err := bare.Fingerprint(d)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if got != want {
		t.Fatal("history fields changed the fingerprint")
	}
}

func TestSpecRoundTripsThroughJSON(t *testing.T) {
	spec := v2Spec(t)
	data, err := spec.MarshalCanonical()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := migrate.ParseSpec(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	d := sqliteDialect(t)
	a, _ := spec.Fingerprint(d)
	b, _ := back.Fingerprint(d)
	if a != b {
		t.Fatal("snapshot round trip changed the fingerprint")
	}

	// History rides the snapshot file too
	world := back.Table("worlds")
	if world == nil || world.Column("world_seed") == nil {
		t.Fatal("worlds table lost in round trip")
	}
	if len(world.Column("world_seed").Was) != 1 || world.Column("world_seed").Was[0] != "seed" {
		t.Fatalf("was history lost, got %v", world.Column("world_seed").Was)
	}
}

func TestDiffResolvesRenamesFromHistory(t *testing.T) {
	ops, demands, err := migrate.Diff(v1Spec(t), v2Spec(t), nil)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(demands) != 0 {
		t.Fatalf("history left demands %v", demands)
	}
	found := false
	for _, op := range ops {
		change, ok := op.(migrate.TableChange)
		if !ok || change.Table.Name != "worlds" {
			continue
		}
		if change.Renames["world_seed"] != "seed" {
			t.Fatalf("rename not resolved, got %v", change.Renames)
		}
		found = true
	}
	if !found {
		t.Fatal("worlds change missing from ops")
	}
}

func TestDiffResolvesDropsFromReserved(t *testing.T) {
	from := &migrate.Spec{Tables: []*migrate.TableSpec{{
		Name: "players",
		Columns: []*migrate.ColumnSpec{
			{Name: "id", Types: map[string]string{"sqlite": "text"}, PK: true, NotNull: true},
			{Name: "legacy_flags", Types: map[string]string{"sqlite": "text"}},
		},
	}}}
	to := &migrate.Spec{Tables: []*migrate.TableSpec{{
		Name:     "players",
		Reserved: []string{"legacy_flags"},
		Columns: []*migrate.ColumnSpec{
			{Name: "id", Types: map[string]string{"sqlite": "text"}, PK: true, NotNull: true},
		},
	}}}
	ops, demands, err := migrate.Diff(from, to, nil)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(demands) != 0 {
		t.Fatalf("reserved name left demands %v", demands)
	}
	if len(ops) != 1 {
		t.Fatalf("want one change, got %v", ops)
	}
	change, ok := ops[0].(migrate.TableChange)
	if !ok || len(change.Drops) != 1 || change.Drops[0] != "legacy_flags" {
		t.Fatalf("drop not resolved, got %+v", ops[0])
	}
}

func TestDiffResolvesTableRenameFromHistory(t *testing.T) {
	from := &migrate.Spec{Tables: []*migrate.TableSpec{{
		Name: "tomes",
		Columns: []*migrate.ColumnSpec{
			{Name: "id", Types: map[string]string{"sqlite": "text"}, PK: true, NotNull: true},
		},
	}}}
	to := &migrate.Spec{Tables: []*migrate.TableSpec{{
		Name: "books",
		Was:  []string{"tomes"},
		Columns: []*migrate.ColumnSpec{
			{Name: "id", Types: map[string]string{"sqlite": "text"}, PK: true, NotNull: true},
		},
	}}}
	ops, demands, err := migrate.Diff(from, to, nil)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(demands) != 0 {
		t.Fatalf("table history left demands %v", demands)
	}
	if len(ops) != 1 {
		t.Fatalf("want one change, got %v", ops)
	}
	change, ok := ops[0].(migrate.TableChange)
	if !ok || change.From != "tomes" || change.Table.Name != "books" {
		t.Fatalf("table rename not resolved, got %+v", ops[0])
	}
}

func TestDiffDemandsBeforeLoss(t *testing.T) {
	from := v2Spec(t)
	to := v1Spec(t)

	// Shrinking without answers must demand, never guess
	ops, demands, err := migrate.Diff(from, to, nil)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(demands) == 0 {
		t.Fatal("destructive diff raised no demands")
	}
	for _, op := range ops {
		if _, ok := op.(migrate.DropTable); ok {
			t.Fatal("unconfirmed table drop emitted")
		}
	}

	// Full answers silence every demand
	_, demands, err = migrate.Diff(from, to, &migrate.Resolution{
		DropTables:  []string{"realms"},
		DropColumns: map[string][]string{"players": {"level"}},
		Renames: map[string]map[string]string{
			"worlds": {"seed": "world_seed"},
		},
	})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(demands) != 0 {
		t.Fatalf("resolved diff still demands %v", demands)
	}
}

func TestDiffEmptyOnIdenticalSpecs(t *testing.T) {
	ops, demands, err := migrate.Diff(v1Spec(t), v1Spec(t), nil)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(ops) != 0 || len(demands) != 0 {
		t.Fatalf("identical specs produced work, %v %v", ops, demands)
	}
}

func TestScaffoldRendersCompilableSource(t *testing.T) {
	files, demands, err := migrate.Scaffold(migrate.ScaffoldRequest{
		Package: "migrations",
		Name:    "second_release",
		Ordinal: 1,
		From:    v1Spec(t),
		Head:    v2Spec(t),
	})
	if err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	if len(demands) != 0 {
		t.Fatalf("unexpected demands %v", demands)
	}
	source, ok := files["0001_second_release.go"]
	if !ok {
		t.Fatalf("missing source file, got %v", keys(files))
	}
	for _, want := range []string{
		"package migrations",
		`mustSnapshot("0001_second_release.snapshot.json")`,
		"Ordinal: 1",
		`Renames: map[string]string{"world_seed": "seed"}`,
	} {
		if !strings.Contains(string(source), want) {
			t.Fatalf("scaffold missing %q in\n%s", want, source)
		}
	}
	snapshot, ok := files["0001_second_release.snapshot.json"]
	if !ok {
		t.Fatal("missing snapshot file")
	}
	if _, err := migrate.ParseSpec(snapshot); err != nil {
		t.Fatalf("snapshot unparsable: %v", err)
	}
}

func TestScaffoldRefusesUnresolvedDemands(t *testing.T) {
	files, demands, err := migrate.Scaffold(migrate.ScaffoldRequest{
		Package: "migrations",
		Name:    "shrink",
		Ordinal: 1,
		From:    v2Spec(t),
		Head:    v1Spec(t),
	})
	if err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	if len(files) != 0 {
		t.Fatal("unresolved scaffold produced files")
	}
	if len(demands) == 0 {
		t.Fatal("unresolved scaffold raised no demands")
	}
}

func TestRegistryBootstrapRenders(t *testing.T) {
	data, err := migrate.RenderRegistryBootstrap("migrations")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	for _, want := range []string{
		"package migrations",
		"//go:embed *.snapshot.json",
		`mustSnapshot("head.snapshot.json")`,
		`snapshots.ReadFile("genesis.snapshot.json")`,
		"var Registry = &migrate.Registry{Genesis: genesis()}",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("bootstrap missing %q in\n%s", want, data)
		}
	}
}

func TestSpecOfDBMatchesSpecOfModels(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	spec := v1Spec(t)
	d := sqliteDialect(t)
	for _, table := range spec.Tables {
		sql, err := d.CreateTableSQL(table)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if err := db.Exec(sql).Error; err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
		for _, idx := range table.Indexes {
			if err := db.Exec(d.CreateIndexSQL(table.Name, idx)).Error; err != nil {
				t.Fatalf("index: %v", err)
			}
		}
	}
	observed, err := migrate.SpecOfDB(db)
	if err != nil {
		t.Fatalf("spec of db: %v", err)
	}
	want, _ := spec.Fingerprint(d)
	got, _ := observed.Fingerprint(d)
	if got != want {
		t.Fatal("introspection disagrees with the model spec")
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
