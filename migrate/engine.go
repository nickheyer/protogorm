package migrate

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Maps an unledgered database onto a chain position
type Baseline interface {
	// Last ordinal the observed schema counts as applied
	// Errors refuse the database with their message
	Detect(db *gorm.DB, observed *Spec) (int, error)
}

// Moves one database onto the head schema
type Engine struct {
	DB       *gorm.DB
	Registry *Registry
	// Desired head schema from the generated models
	Head *Spec
	// Nil refuses unledgered non empty databases
	Baseline Baseline
	// Version stamp written onto ledger rows
	AppVersion string
	// Pre migration backup destination, empty skips
	BackupPath string
	// False only reports pending work
	Apply bool
	// Nil logs nowhere
	Log func(format string, args ...any)
}

// What one run found and did
type Report struct {
	// Database was empty and created at head
	Fresh bool
	// Chain position a baseline detect resumed from
	Baseline int
	// Migration names applied this run
	Applied []string
	// Migration names still waiting, apply off only
	Pending []string
	// Head fingerprint for the active dialect
	Fingerprint string
}

func (e *Engine) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log(format, args...)
	}
}

// Runs verification and any pending migrations
// Every path ends by proving the head fingerprint
func (e *Engine) Run(ctx context.Context) (*Report, error) {
	if e.DB == nil || e.Registry == nil || e.Head == nil {
		return nil, fmt.Errorf("engine needs db, registry, and head spec")
	}
	d, err := DialectFor(e.DB)
	if err != nil {
		return nil, err
	}
	headFP, err := e.Head.Fingerprint(d)
	if err != nil {
		return nil, err
	}
	if err := e.validateChain(d, headFP); err != nil {
		return nil, err
	}

	report := &Report{Fingerprint: headFP}
	db := e.DB.WithContext(ctx)

	hasLedger := db.Migrator().HasTable(LedgerTable)
	observed, err := SpecOfDB(db)
	if err != nil {
		return nil, err
	}

	// Empty databases are created at head in one step
	if len(observed.Tables) == 0 && ledgerEmpty(db, hasLedger) {
		if !e.Apply {
			report.Pending = []string{"fresh install"}
			return report, nil
		}
		if err := e.freshInstall(db, d); err != nil {
			return nil, err
		}
		report.Fresh = true
		return report, e.verifyHead(db, d, headFP)
	}

	applied, err := e.resolveApplied(db, d, hasLedger, observed)
	if err != nil {
		return nil, err
	}
	report.Baseline = applied

	pending := e.Registry.All()[applied:]
	if len(pending) == 0 {
		return report, e.verifyHead(db, d, headFP)
	}
	if !e.Apply {
		for _, m := range pending {
			report.Pending = append(report.Pending, m.Name)
		}
		return report, nil
	}

	if err := e.backup(db, d); err != nil {
		return nil, err
	}
	for _, m := range pending {
		if err := e.applyOne(ctx, d, m); err != nil {
			return nil, fmt.Errorf("migration %s: %w", m.Name, err)
		}
		report.Applied = append(report.Applied, m.Name)
	}
	return report, e.verifyHead(db, d, headFP)
}

// Chain must be contiguous and end exactly at head
// A mismatch means models moved without a migration
func (e *Engine) validateChain(d Dialect, headFP string) error {
	all := e.Registry.All()
	if len(all) == 0 {
		return nil
	}
	last := all[len(all)-1]
	lastFP, err := last.Target.Fingerprint(d)
	if err != nil {
		return err
	}
	if lastFP != headFP {
		return fmt.Errorf("model schema drifted past migration %s, scaffold a new migration", last.Name)
	}
	return nil
}

// True when no ledger rows exist
func ledgerEmpty(db *gorm.DB, hasLedger bool) bool {
	if !hasLedger {
		return true
	}
	var count int64
	if err := db.Table(LedgerTable).Count(&count).Error; err != nil {
		return false
	}
	return count == 0
}

// Creates every head table then stamps the whole chain
func (e *Engine) freshInstall(db *gorm.DB, d Dialect) error {
	e.logf("creating fresh schema, %d tables", len(e.Head.Tables))
	return db.Transaction(func(tx *gorm.DB) error {
		for _, t := range e.Head.Tables {
			sql, err := d.CreateTableSQL(t)
			if err != nil {
				return err
			}
			if err := tx.Exec(sql).Error; err != nil {
				return fmt.Errorf("create %s: %w", t.Name, err)
			}
			for _, idx := range t.Indexes {
				if err := tx.Exec(d.CreateIndexSQL(t.Name, idx)).Error; err != nil {
					return fmt.Errorf("index %s: %w", idx.Name, err)
				}
			}
		}
		if err := ensureLedger(tx, d); err != nil {
			return err
		}
		return e.stampThrough(tx, d, e.Registry.Len(), 0)
	})
}

// Creates the ledger table through the dialect ddl
func ensureLedger(tx *gorm.DB, d Dialect) error {
	if tx.Migrator().HasTable(LedgerTable) {
		return nil
	}
	sql, err := d.CreateTableSQL(ledgerSpec(d))
	if err != nil {
		return err
	}
	return tx.Exec(sql).Error
}

// Records chain rows up to and including ordinal
func (e *Engine) stampThrough(tx *gorm.DB, d Dialect, ordinal int, durationMs int64) error {
	for i := 1; i <= ordinal; i++ {
		m := e.Registry.At(i)
		fp, err := m.Target.Fingerprint(d)
		if err != nil {
			return err
		}
		row := &ledgerRow{
			Ordinal:     int64(m.Ordinal),
			Name:        m.Name,
			Fingerprint: fp,
			AppVersion:  e.AppVersion,
			AppliedAt:   time.Now().UTC(),
			DurationMs:  durationMs,
		}
		if err := tx.Create(row).Error; err != nil {
			return err
		}
	}
	return nil
}

// Finds how much of the chain the database already has
// Recorded rows verify, silent ledgers resolve by fingerprint
func (e *Engine) resolveApplied(db *gorm.DB, d Dialect, hasLedger bool, observed *Spec) (int, error) {
	if hasLedger && !ledgerEmpty(db, true) {
		return e.verifyLedger(db, d, observed)
	}

	// Older fresh installs sit at some past chain position
	if applied, ok, err := e.resolvePosition(d, observed); err != nil {
		return 0, err
	} else if ok {
		e.logf("unrecorded database matches chain position %d", applied)
		return applied, e.stampBaseline(db, d, applied)
	}

	if e.Baseline == nil {
		return 0, fmt.Errorf("database matches no known schema and has no migration ledger, refusing to guess")
	}
	applied, err := e.Baseline.Detect(db, observed)
	if err != nil {
		return 0, fmt.Errorf("baseline detect failed: %w", err)
	}
	if applied < 0 || applied > e.Registry.Len() {
		return 0, fmt.Errorf("baseline detect returned ordinal %d outside the chain", applied)
	}
	e.logf("pre framework database resumes after ordinal %d", applied)
	return applied, e.stampBaseline(db, d, applied)
}

// Matches an observed schema onto a chain position
// Newest positions win when snapshots ever repeat
func (e *Engine) resolvePosition(d Dialect, observed *Spec) (int, bool, error) {
	observedFP, err := observed.Fingerprint(d)
	if err != nil {
		return 0, false, err
	}
	for i := e.Registry.Len(); i >= 1; i-- {
		fp, err := e.Registry.At(i).Target.Fingerprint(d)
		if err != nil {
			return 0, false, err
		}
		if fp == observedFP {
			return i, true, nil
		}
	}
	genesis := e.Registry.Genesis
	if genesis == nil {
		genesis = e.Head
	}
	fp, err := genesis.Fingerprint(d)
	if err != nil {
		return 0, false, err
	}
	if fp == observedFP {
		return 0, true, nil
	}
	return 0, false, nil
}

// Records the resolved position into a fresh ledger
func (e *Engine) stampBaseline(db *gorm.DB, d Dialect, applied int) error {
	if !e.Apply {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := ensureLedger(tx, d); err != nil {
			return err
		}
		return e.stampThrough(tx, d, applied, 0)
	})
}

// Replays recorded rows against the registry
// The live schema must match the last recorded target
func (e *Engine) verifyLedger(db *gorm.DB, d Dialect, observed *Spec) (int, error) {
	var rows []ledgerRow
	if err := db.Table(LedgerTable).Order("ordinal ASC").Find(&rows).Error; err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("migration ledger exists but is empty, restore from backup")
	}
	if len(rows) > e.Registry.Len() {
		return 0, fmt.Errorf("database is ahead of this build, ledger has %d migrations but the chain has %d", len(rows), e.Registry.Len())
	}
	for i, row := range rows {
		m := e.Registry.At(i + 1)
		if int64(m.Ordinal) != row.Ordinal || m.Name != row.Name {
			return 0, fmt.Errorf("ledger row %d is %s, chain says %s", row.Ordinal, row.Name, m.Name)
		}
		fp, err := m.Target.Fingerprint(d)
		if err != nil {
			return 0, err
		}
		if fp != row.Fingerprint {
			return 0, fmt.Errorf("migration %s was applied from a different snapshot", m.Name)
		}
	}
	last := e.Registry.At(len(rows))
	lastFP, err := last.Target.Fingerprint(d)
	if err != nil {
		return 0, err
	}
	observedFP, err := observed.Fingerprint(d)
	if err != nil {
		return 0, err
	}
	if observedFP != lastFP {
		return 0, fmt.Errorf("live schema does not match migration %s, the database was changed outside migrations", last.Name)
	}
	return len(rows), nil
}

// Copies the database aside before any pending work
func (e *Engine) backup(db *gorm.DB, d Dialect) error {
	if e.BackupPath == "" {
		return nil
	}
	e.logf("backing up database to %s", e.BackupPath)
	if err := d.Backup(db, e.BackupPath); err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}
	return nil
}

// Applies one migration proving its target before commit
func (e *Engine) applyOne(ctx context.Context, d Dialect, m *Migration) error {
	e.logf("applying migration %d %s", m.Ordinal, m.Name)
	start := time.Now()

	targetFP, err := m.Target.Fingerprint(d)
	if err != nil {
		return err
	}

	// One pinned connection holds session state end to end
	return e.DB.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		if err := d.Begin(conn); err != nil {
			return err
		}
		defer d.End(conn)

		run := func(tx *gorm.DB) error {
			for _, op := range m.Ops {
				statements, transform, err := lowerOp(d, op)
				if err != nil {
					return err
				}
				for _, sql := range statements {
					if err := tx.Exec(sql).Error; err != nil {
						return fmt.Errorf("exec %q: %w", sql, err)
					}
				}
				if transform != nil {
					if err := transform.Fn(tx, d); err != nil {
						return fmt.Errorf("transform %s: %w", transform.Name, err)
					}
				}
			}
			if err := d.Check(tx); err != nil {
				return err
			}
			reached, err := SpecOfDB(tx)
			if err != nil {
				return err
			}
			reachedFP, err := reached.Fingerprint(d)
			if err != nil {
				return err
			}
			if reachedFP != targetFP {
				return fmt.Errorf("schema landed off target, rolled back")
			}
			row := &ledgerRow{
				Ordinal:     int64(m.Ordinal),
				Name:        m.Name,
				Fingerprint: targetFP,
				AppVersion:  e.AppVersion,
				AppliedAt:   time.Now().UTC(),
				DurationMs:  time.Since(start).Milliseconds(),
			}
			return tx.Create(row).Error
		}

		if d.TransactionalDDL() {
			return conn.Transaction(run)
		}
		// Engines without ddl rollback run exposed
		return run(conn)
	})
}

// Final proof the database is exactly the head schema
func (e *Engine) verifyHead(db *gorm.DB, d Dialect, headFP string) error {
	observed, err := SpecOfDB(db)
	if err != nil {
		return err
	}
	observedFP, err := observed.Fingerprint(d)
	if err != nil {
		return err
	}
	if observedFP != headFP {
		return fmt.Errorf("database schema does not match the build, restore from backup or scaffold a migration")
	}
	return nil
}
