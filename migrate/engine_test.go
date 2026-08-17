package migrate_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/nickheyer/protogorm/migrate"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// First release shape of the test app
type playerV1 struct {
	ID    string `gorm:"primaryKey;column:id"`
	Name  string `gorm:"column:name;not null;uniqueIndex:idx_players_name"`
	Coins int64  `gorm:"column:coins;not null;default:0"`
	Admin bool   `gorm:"column:admin;not null;default:false"`
}

func (playerV1) TableName() string { return "players" }

type worldV1 struct {
	ID   string `gorm:"primaryKey;column:id"`
	Name string `gorm:"column:name;not null"`
	Seed int64  `gorm:"column:seed"`
}

func (worldV1) TableName() string { return "worlds" }

// Second release adds, renames, and grows a table
type playerV2 struct {
	ID    string `gorm:"primaryKey;column:id"`
	Name  string `gorm:"column:name;not null;uniqueIndex:idx_players_name"`
	Coins int64  `gorm:"column:coins;not null;default:0"`
	Admin bool   `gorm:"column:admin;not null;default:false"`
	Level int64  `gorm:"column:level;not null;default:1"`
}

func (playerV2) TableName() string { return "players" }

type worldV2 struct {
	ID        string `gorm:"primaryKey;column:id"`
	Name      string `gorm:"column:name;not null"`
	WorldSeed int64  `gorm:"column:world_seed"`
}

func (worldV2) TableName() string { return "worlds" }

type realmV2 struct {
	ID    string `gorm:"primaryKey;column:id"`
	Title string `gorm:"column:title;not null"`
}

func (realmV2) TableName() string { return "realms" }

func openDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return db
}

func specOf(t *testing.T, models ...any) *migrate.Spec {
	t.Helper()
	spec, err := migrate.SpecOf(migrate.SpecSource{
		Models:     models,
		Dialectors: []gorm.Dialector{sqlite.Open("file::memory:")},
	})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	return spec
}

func v1Spec(t *testing.T) *migrate.Spec { return specOf(t, &playerV1{}, &worldV1{}) }

func v2Spec(t *testing.T) *migrate.Spec { return specOf(t, &playerV2{}, &worldV2{}, &realmV2{}) }

// Chain holding just the genesis
func v1Registry(t *testing.T) *migrate.Registry {
	t.Helper()
	return &migrate.Registry{Genesis: v1Spec(t)}
}

// Chain moving genesis onto the second release
func v2Registry(t *testing.T) *migrate.Registry {
	t.Helper()
	genesis := v1Spec(t)
	target := v2Spec(t)
	reg := &migrate.Registry{Genesis: genesis}
	ops, demands, err := migrate.Diff(genesis, target, &migrate.Resolution{
		Renames: map[string]map[string]string{
			"worlds": {"world_seed": "seed"},
		},
	})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(demands) > 0 {
		t.Fatalf("unexpected demands %v", demands)
	}
	reg.MustAdd(&migrate.Migration{
		Ordinal: 1,
		Name:    "second_release",
		Target:  target,
		Ops:     ops,
	})
	return reg
}

func run(t *testing.T, db *gorm.DB, reg *migrate.Registry, head *migrate.Spec) *migrate.Report {
	t.Helper()
	report, err := (&migrate.Engine{
		DB:       db,
		Registry: reg,
		Head:     head,
		Apply:    true,
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	return report
}

func fingerprint(t *testing.T, db *gorm.DB) string {
	t.Helper()
	spec, err := migrate.SpecOfDB(db)
	if err != nil {
		t.Fatalf("spec of db: %v", err)
	}
	d, err := migrate.DialectByName("sqlite")
	if err != nil {
		t.Fatalf("dialect: %v", err)
	}
	fp, err := spec.Fingerprint(d)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	return fp
}

func TestFreshInstallLandsOnHead(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "fresh.db"))
	report := run(t, db, v1Registry(t), v1Spec(t))
	if !report.Fresh {
		t.Fatal("expected a fresh install")
	}
	if fingerprint(t, db) != report.Fingerprint {
		t.Fatal("fresh install does not match head")
	}

	// Second run finds nothing to do
	again := run(t, db, v1Registry(t), v1Spec(t))
	if again.Fresh || len(again.Applied) > 0 {
		t.Fatalf("second run did work, %+v", again)
	}
}

func TestMigratedEqualsFresh(t *testing.T) {
	dir := t.TempDir()

	// One database lives through both releases
	upgraded := openDB(t, filepath.Join(dir, "upgraded.db"))
	run(t, upgraded, v1Registry(t), v1Spec(t))
	if err := upgraded.Exec(
		"INSERT INTO worlds (id, name, seed) VALUES ('w1', 'overworld', 42)").Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}
	if err := upgraded.Exec(
		"INSERT INTO players (id, name, coins, admin) VALUES ('p1', 'steve', 7, 1)").Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}
	report := run(t, upgraded, v2Registry(t), v2Spec(t))
	if len(report.Applied) != 1 || report.Applied[0] != "second_release" {
		t.Fatalf("unexpected applied %v", report.Applied)
	}

	// Another database installs the second release fresh
	fresh := openDB(t, filepath.Join(dir, "fresh.db"))
	run(t, fresh, v2Registry(t), v2Spec(t))

	if fingerprint(t, upgraded) != fingerprint(t, fresh) {
		t.Fatal("migrated schema differs from fresh install")
	}

	// Rows rode the rename and gained defaults
	var seed int64
	if err := upgraded.Raw("SELECT world_seed FROM worlds WHERE id = 'w1'").Scan(&seed).Error; err != nil {
		t.Fatalf("read seed: %v", err)
	}
	if seed != 42 {
		t.Fatalf("world seed lost, got %d", seed)
	}
	var level int64
	if err := upgraded.Raw("SELECT level FROM players WHERE id = 'p1'").Scan(&level).Error; err != nil {
		t.Fatalf("read level: %v", err)
	}
	if level != 1 {
		t.Fatalf("level default missing, got %d", level)
	}
}

func TestDriftRefusesToServe(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "drift.db"))
	run(t, db, v1Registry(t), v1Spec(t))
	if err := db.Exec("ALTER TABLE players ADD COLUMN hacked text").Error; err != nil {
		t.Fatalf("alter: %v", err)
	}
	_, err := (&migrate.Engine{
		DB:       db,
		Registry: v1Registry(t),
		Head:     v1Spec(t),
		Apply:    true,
	}).Run(context.Background())
	if err == nil {
		t.Fatal("drifted database must refuse")
	}
}

func TestNewerDatabaseRefused(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "ahead.db"))
	run(t, db, v2Registry(t), v2Spec(t))
	_, err := (&migrate.Engine{
		DB:       db,
		Registry: v1Registry(t),
		Head:     v1Spec(t),
		Apply:    true,
	}).Run(context.Background())
	if err == nil {
		t.Fatal("database ahead of the build must refuse")
	}
}

func TestUnledgeredKnownSchemaResumes(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "silent.db"))
	run(t, db, v1Registry(t), v1Spec(t))
	// A lost ledger must not strand a known schema
	if err := db.Exec("DROP TABLE " + migrate.LedgerTable).Error; err != nil {
		t.Fatalf("drop ledger: %v", err)
	}
	report := run(t, db, v2Registry(t), v2Spec(t))
	if report.Baseline != 0 || len(report.Applied) != 1 {
		t.Fatalf("unexpected report %+v", report)
	}
}

func TestUnknownSchemaRefusedWithoutBaseline(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "stranger.db"))
	if err := db.Exec("CREATE TABLE mystery (id text primary key)").Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err := (&migrate.Engine{
		DB:       db,
		Registry: v1Registry(t),
		Head:     v1Spec(t),
		Apply:    true,
	}).Run(context.Background())
	if err == nil {
		t.Fatal("unknown schema must refuse without a baseline")
	}
}

// Maps the stranger schema onto position zero
type stubBaseline struct{ ordinal int }

func (b stubBaseline) Detect(*gorm.DB, *migrate.Spec) (int, error) { return b.ordinal, nil }

func TestHandBuiltGenesisResolvesByFingerprint(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, filepath.Join(dir, "handmade.db"))

	// Hand built ddl matching the genesis shape exactly
	stmts := []string{
		"CREATE TABLE players (`id` text PRIMARY KEY, `name` text NOT NULL, `coins` integer NOT NULL DEFAULT 0, `admin` numeric NOT NULL DEFAULT false)",
		"CREATE UNIQUE INDEX `idx_players_name` ON players(`name`)",
		"CREATE TABLE worlds (`id` text PRIMARY KEY, `name` text NOT NULL, `seed` integer)",
		"INSERT INTO worlds (id, name, seed) VALUES ('w1', 'legacy', 9)",
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("legacy ddl: %v", err)
		}
	}

	report := run(t, db, v2Registry(t), v2Spec(t))
	if report.Baseline != 0 || len(report.Applied) != 1 {
		t.Fatalf("unexpected report %+v", report)
	}
	var seed int64
	if err := db.Raw("SELECT world_seed FROM worlds WHERE id = 'w1'").Scan(&seed).Error; err != nil {
		t.Fatalf("read seed: %v", err)
	}
	if seed != 9 {
		t.Fatalf("legacy row lost, got %d", seed)
	}
}

// Pre framework shape the intake migration transforms
type legacyPlayer struct {
	ID   string `gorm:"primaryKey;column:id"`
	Name string `gorm:"column:name;not null;uniqueIndex:idx_players_name"`
	Gold int64  `gorm:"column:gold;not null;default:0"`
}

func (legacyPlayer) TableName() string { return "players" }

type legacyWorld struct {
	ID   string `gorm:"primaryKey;column:id"`
	Name string `gorm:"column:name;not null"`
	Seed int64  `gorm:"column:seed"`
}

func (legacyWorld) TableName() string { return "worlds" }

func TestBaselineIntakeTransformsLegacy(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, filepath.Join(dir, "legacy.db"))

	// Legacy schema differing from every known snapshot
	stmts := []string{
		"CREATE TABLE players (`id` text PRIMARY KEY, `name` text NOT NULL, `gold` integer NOT NULL DEFAULT 0)",
		"CREATE UNIQUE INDEX `idx_players_name` ON players(`name`)",
		"CREATE TABLE worlds (`id` text PRIMARY KEY, `name` text NOT NULL, `seed` integer)",
		"INSERT INTO players (id, name, gold) VALUES ('p1', 'alex', 55)",
		"INSERT INTO worlds (id, name, seed) VALUES ('w1', 'legacy', 9)",
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("legacy ddl: %v", err)
		}
	}

	// Intake migration written against the legacy shape
	target := v2Spec(t)
	reg := &migrate.Registry{Genesis: specOf(t, &legacyPlayer{}, &legacyWorld{})}
	reg.MustAdd(&migrate.Migration{
		Ordinal: 1,
		Name:    "legacy_intake",
		Target:  target,
		Ops: []migrate.Op{
			migrate.TableChange{
				Table:   target.Table("players"),
				Adds:    []string{"admin", "level"},
				Renames: map[string]string{"coins": "gold"},
				Copy:    map[string]string{"admin": "0"},
			},
			migrate.TableChange{
				Table:   target.Table("worlds"),
				Renames: map[string]string{"world_seed": "seed"},
			},
			migrate.CreateTable{Table: target.Table("realms")},
		},
	})

	report, err := (&migrate.Engine{
		DB:       db,
		Registry: reg,
		Head:     target,
		Baseline: stubBaseline{ordinal: 0},
		Apply:    true,
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if len(report.Applied) != 1 {
		t.Fatalf("intake did not run, %+v", report)
	}

	var coins int64
	if err := db.Raw("SELECT coins FROM players WHERE id = 'p1'").Scan(&coins).Error; err != nil {
		t.Fatalf("read coins: %v", err)
	}
	if coins != 55 {
		t.Fatalf("gold did not land in coins, got %d", coins)
	}

	// Intaken database matches a fresh second release
	fresh := openDB(t, filepath.Join(dir, "fresh.db"))
	run(t, fresh, v2Registry(t), v2Spec(t))
	if fingerprint(t, db) != fingerprint(t, fresh) {
		t.Fatal("intaken schema differs from fresh install")
	}
}

func TestBackupWrittenBeforeApply(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, filepath.Join(dir, "app.db"))
	run(t, db, v1Registry(t), v1Spec(t))

	backup := filepath.Join(dir, "app.pre.bak")
	_, err := (&migrate.Engine{
		DB:         db,
		Registry:   v2Registry(t),
		Head:       v2Spec(t),
		BackupPath: backup,
		Apply:      true,
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
}

func TestCheckModeReportsWithoutTouching(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "check.db"))
	run(t, db, v1Registry(t), v1Spec(t))
	before := fingerprint(t, db)

	report, err := (&migrate.Engine{
		DB:       db,
		Registry: v2Registry(t),
		Head:     v2Spec(t),
		Apply:    false,
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if len(report.Pending) != 1 {
		t.Fatalf("expected one pending, got %v", report.Pending)
	}
	if fingerprint(t, db) != before {
		t.Fatal("check mode changed the database")
	}
}

func TestTransformRunsInsideMigration(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "transform.db"))
	run(t, db, v1Registry(t), v1Spec(t))
	if err := db.Exec(
		"INSERT INTO players (id, name, coins, admin) VALUES ('p1', 'STEVE', 0, 0)").Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	genesis := v1Spec(t)
	target := v2Spec(t)
	reg := &migrate.Registry{Genesis: genesis}
	ops, _, err := migrate.Diff(genesis, target, &migrate.Resolution{
		Renames: map[string]map[string]string{"worlds": {"world_seed": "seed"}},
	})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	ops = append(ops, migrate.Transform{
		Name: "lowercase_names",
		Fn: func(tx *gorm.DB, _ migrate.Dialect) error {
			return tx.Exec("UPDATE players SET name = lower(name)").Error
		},
	})
	reg.MustAdd(&migrate.Migration{Ordinal: 1, Name: "second_release", Target: target, Ops: ops})

	run(t, db, reg, target)
	var name string
	if err := db.Raw("SELECT name FROM players WHERE id = 'p1'").Scan(&name).Error; err != nil {
		t.Fatalf("read name: %v", err)
	}
	if name != "steve" {
		t.Fatalf("transform missed, got %q", name)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "rollback.db"))
	run(t, db, v1Registry(t), v1Spec(t))
	before := fingerprint(t, db)

	genesis := v1Spec(t)
	target := v2Spec(t)
	reg := &migrate.Registry{Genesis: genesis}
	ops, _, err := migrate.Diff(genesis, target, &migrate.Resolution{
		Renames: map[string]map[string]string{"worlds": {"world_seed": "seed"}},
	})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	ops = append(ops, migrate.Exec{SQL: []string{"THIS IS NOT SQL"}})
	reg.MustAdd(&migrate.Migration{Ordinal: 1, Name: "second_release", Target: target, Ops: ops})

	_, err = (&migrate.Engine{DB: db, Registry: reg, Head: target, Apply: true}).Run(context.Background())
	if err == nil {
		t.Fatal("broken migration must fail")
	}
	if fingerprint(t, db) != before {
		t.Fatal("failed migration left changes behind")
	}
}
