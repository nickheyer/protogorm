package migrate

import (
	"fmt"
	"time"
)

// Ledger table recording every applied migration
const LedgerTable = "_protogorm_migrations"

// One applied migration on record
type ledgerRow struct {
	Ordinal     int64     `gorm:"primaryKey;autoIncrement:false;column:ordinal"`
	Name        string    `gorm:"column:name;not null"`
	Fingerprint string    `gorm:"column:fingerprint;not null"`
	AppVersion  string    `gorm:"column:app_version"`
	AppliedAt   time.Time `gorm:"column:applied_at;not null"`
	DurationMs  int64     `gorm:"column:duration_ms;not null"`
}

func (ledgerRow) TableName() string { return LedgerTable }

// Ledger table shape rendered through one dialect
func ledgerSpec(d Dialect) *TableSpec {
	col := func(name string, kind LogicalType, notNull, pk bool) *ColumnSpec {
		return &ColumnSpec{
			Name:    name,
			Types:   map[string]string{d.Name(): d.TypeOf(LogicalColumn{Kind: kind, PK: pk})},
			NotNull: notNull || pk,
			PK:      pk,
		}
	}
	return &TableSpec{
		Name: LedgerTable,
		Columns: []*ColumnSpec{
			col("ordinal", TypeInt64, true, true),
			col("name", TypeText, true, false),
			col("fingerprint", TypeText, true, false),
			col("app_version", TypeText, false, false),
			col("applied_at", TypeTime, true, false),
			col("duration_ms", TypeInt64, true, false),
		},
	}
}

// One step moving the schema onto its target snapshot
type Migration struct {
	// Position in the chain starting at one
	Ordinal int
	// Short stable name like add_lobby_flags
	Name string
	// Committed snapshot the database shows afterwards
	Target *Spec
	// Ordered changes landing the target
	Ops []Op
}

// Ordered chain of every known migration
type Registry struct {
	// First framework era schema, position zero
	// Nil means the head spec is the genesis
	Genesis *Spec

	migrations []*Migration
}

// Adds one migration keeping the chain contiguous
func (r *Registry) Add(m *Migration) error {
	if len(r.migrations) == 0 && r.Genesis == nil {
		return fmt.Errorf("chain needs a genesis snapshot before migration %s", m.Name)
	}
	if m.Ordinal != len(r.migrations)+1 {
		return fmt.Errorf("migration %s has ordinal %d, want %d", m.Name, m.Ordinal, len(r.migrations)+1)
	}
	if m.Name == "" {
		return fmt.Errorf("migration %d needs a name", m.Ordinal)
	}
	if m.Target == nil {
		return fmt.Errorf("migration %s needs a target snapshot", m.Name)
	}
	if len(m.Ops) == 0 {
		return fmt.Errorf("migration %s has no ops", m.Name)
	}
	r.migrations = append(r.migrations, m)
	return nil
}

// Adds one migration or panics, for package init blocks
func (r *Registry) MustAdd(m *Migration) {
	if err := r.Add(m); err != nil {
		panic(err)
	}
}

// All migrations in chain order
func (r *Registry) All() []*Migration {
	return r.migrations
}

// Chain length
func (r *Registry) Len() int {
	return len(r.migrations)
}

// Migration at one ordinal
func (r *Registry) At(ordinal int) *Migration {
	if ordinal < 1 || ordinal > len(r.migrations) {
		return nil
	}
	return r.migrations[ordinal-1]
}
