package migrate_test

import (
	"strings"
	"testing"

	"github.com/nickheyer/protogorm/migrate"
)

// Target shape exercised without a live server
func pgTable() *migrate.TableSpec {
	return &migrate.TableSpec{
		Name: "players",
		Columns: []*migrate.ColumnSpec{
			{Name: "id", Types: map[string]string{"postgres": "text"}, PK: true, NotNull: true},
			{Name: "name", Types: map[string]string{"postgres": "text"}, NotNull: true},
			{Name: "coins", Types: map[string]string{"postgres": "bigint"}, NotNull: true},
			{Name: "level", Types: map[string]string{"postgres": "bigint"}, NotNull: true},
		},
		Indexes: []*migrate.IndexSpec{
			{Name: "idx_players_name", Columns: []string{"name"}, Unique: true},
		},
	}
}

func TestPostgresPlanAltersInPlace(t *testing.T) {
	d, err := migrate.DialectByName("postgres")
	if err != nil {
		t.Fatalf("dialect: %v", err)
	}
	plan, err := d.AlterPlan(&migrate.TableChange{
		Table:   pgTable(),
		Adds:    []string{"level"},
		Renames: map[string]string{"coins": "gold"},
		Drops:   []string{"legacy_flags"},
		Copy:    map[string]string{"level": "1"},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	joined := strings.Join(plan, "\n")

	for _, want := range []string{
		`ADD COLUMN "level" bigint`,
		`UPDATE "players" SET "level" = 1`,
		`RENAME COLUMN "gold" TO "coins"`,
		`DROP COLUMN "legacy_flags"`,
		`ALTER COLUMN "level" SET NOT NULL`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("plan missing %q in\n%s", want, joined)
		}
	}

	// Backfilled column must not add as not null
	for _, sql := range plan {
		if strings.Contains(sql, "ADD COLUMN") && strings.Contains(sql, "NOT NULL") {
			t.Fatalf("backfilled add carried not null, %s", sql)
		}
	}

	// Backfill must run before the tightening alter
	backfill := indexOf(plan, "UPDATE")
	tighten := indexOf(plan, "SET NOT NULL")
	if backfill < 0 || tighten < 0 || backfill > tighten {
		t.Fatalf("bad statement order\n%s", joined)
	}
}

func TestPostgresTypeNormalization(t *testing.T) {
	d, err := migrate.DialectByName("postgres")
	if err != nil {
		t.Fatalf("dialect: %v", err)
	}
	cases := map[string]string{
		"int8":                        "bigint",
		"BIGSERIAL":                   "bigint",
		"character varying(64)":       "varchar",
		"timestamp without time zone": "timestamp",
		"bool":                        "boolean",
	}
	for in, want := range cases {
		if got := d.NormalizeType(in); got != want {
			t.Fatalf("normalize %q = %q, want %q", in, got, want)
		}
	}
}

func indexOf(list []string, needle string) int {
	for i, s := range list {
		if strings.Contains(s, needle) {
			return i
		}
	}
	return -1
}
