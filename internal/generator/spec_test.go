// Locks descriptor derived specs against the kitchen sink
package generator_test

import (
	"testing"

	"github.com/nickheyer/protogorm/internal/generator"
	"github.com/nickheyer/protogorm/migrate"
)

func buildKitchenSpec(t *testing.T) *migrate.Spec {
	t.Helper()
	spec, err := generator.BuildSpec(compileModels(t), migrate.Dialects())
	if err != nil {
		t.Fatalf("build spec: %v", err)
	}
	return spec
}

func TestSpecTables(t *testing.T) {
	spec := buildKitchenSpec(t)
	if len(spec.Tables) != 3 {
		t.Fatalf("got %d tables, want 3", len(spec.Tables))
	}
	for _, name := range []string{"authors", "books", "reviews"} {
		if spec.Table(name) == nil {
			t.Fatalf("table %s missing", name)
		}
	}
}

func TestSpecColumnTypes(t *testing.T) {
	spec := buildKitchenSpec(t)
	cases := []struct {
		table    string
		column   string
		sqlite   string
		postgres string
	}{
		{"authors", "id", "text", "text"},
		{"authors", "created_at", "datetime", "timestamptz"},
		{"books", "genre", "integer", "integer"},
		{"books", "tags", "text", "text"},
		{"books", "attrs", "text", "text"},
		{"books", "meta", "text", "text"},
		{"books", "revisions", "text", "text"},
		{"books", "published_at", "datetime", "timestamptz"},
		{"reviews", "id", "integer", "bigserial"},
		{"reviews", "stars", "integer", "bigint"},
	}
	for _, c := range cases {
		col := spec.Table(c.table).Column(c.column)
		if col == nil {
			t.Fatalf("column %s.%s missing", c.table, c.column)
		}
		if got := col.Types["sqlite"]; got != c.sqlite {
			t.Errorf("%s.%s sqlite type %q, want %q", c.table, c.column, got, c.sqlite)
		}
		if got := col.Types["postgres"]; got != c.postgres {
			t.Errorf("%s.%s postgres type %q, want %q", c.table, c.column, got, c.postgres)
		}
	}
}

func TestSpecColumnShape(t *testing.T) {
	spec := buildKitchenSpec(t)

	id := spec.Table("authors").Column("id")
	if !id.PK || !id.NotNull {
		t.Errorf("authors.id shape wrong, %+v", id)
	}
	email := spec.Table("authors").Column("email")
	if !email.NotNull || email.Unique {
		t.Errorf("authors.email shape wrong, %+v", email)
	}

	stars := spec.Table("reviews").Column("stars")
	if !stars.NotNull || stars.Default != "0" {
		t.Errorf("reviews.stars shape wrong, %+v", stars)
	}
	rid := spec.Table("reviews").Column("id")
	if !rid.PK || !rid.AutoInc {
		t.Errorf("reviews.id shape wrong, %+v", rid)
	}

	// Skip and relation fields never become columns
	if spec.Table("books").Column("cache_hint") != nil {
		t.Error("skip field became a column")
	}
	if spec.Table("books").Column("author") != nil {
		t.Error("relation field became a column")
	}
}

func TestSpecIndexes(t *testing.T) {
	spec := buildKitchenSpec(t)

	email := spec.Table("authors").Index("idx_authors_email")
	if email == nil || !email.Unique || len(email.Columns) != 1 || email.Columns[0] != "email" {
		t.Fatalf("authors email index wrong, %+v", email)
	}
	handle := spec.Table("authors").Index("idx_authors_handle")
	if handle == nil || !handle.Unique || handle.Where != "handle != ''" {
		t.Fatalf("authors handle index wrong, %+v", handle)
	}
	for _, name := range []string{"idx_books_title", "idx_books_author_id"} {
		idx := spec.Table("books").Index(name)
		if idx == nil || idx.Unique {
			t.Fatalf("books index %s wrong, %+v", name, idx)
		}
	}
}

func TestSpecHistory(t *testing.T) {
	spec := buildKitchenSpec(t)

	books := spec.Table("books")
	if len(books.Was) != 1 || books.Was[0] != "tomes" {
		t.Errorf("books was history wrong, %v", books.Was)
	}
	if len(books.Reserved) != 1 || books.Reserved[0] != "old_isbn" {
		t.Errorf("books reserved wrong, %v", books.Reserved)
	}
	title := books.Column("title")
	if len(title.Was) != 1 || title.Was[0] != "headline" {
		t.Errorf("title was history wrong, %v", title.Was)
	}
}
