// Proves descriptor specs agree with gorm's own parse
package generator

import (
	"context"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/nickheyer/protogorm/internal/testproto"
	"github.com/nickheyer/protogorm/migrate"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Serializer stub so parse accepts tspb tags
type stubSerializer struct{}

func (stubSerializer) Scan(context.Context, *schema.Field, reflect.Value, any) error {
	return nil
}

func (stubSerializer) Value(context.Context, *schema.Field, reflect.Value, any) (any, error) {
	return nil, nil
}

func init() {
	schema.RegisterSerializer("tspb", stubSerializer{})
}

// Hand mirrors of the kitchen sink generator output
// Tags are proven against computeTags before use
type parityMeta struct {
	Publisher string
	Edition   int32
}

type parityGenre int32

type parityAuthor struct {
	Id        string                 `gorm:"column:id;primaryKey"`
	Email     string                 `gorm:"column:email;not null;uniqueIndex"`
	Name      string                 `gorm:"column:name"`
	ApiKey    string                 `gorm:"column:api_key"`
	CreatedAt *timestamppb.Timestamp `gorm:"column:created_at;serializer:tspb;autoCreateTime"`
	UpdatedAt *timestamppb.Timestamp `gorm:"column:updated_at;serializer:tspb;autoUpdateTime"`
	Handle    string                 `gorm:"column:handle;uniqueIndex:idx_authors_handle,where:handle != ''"`
}

func (*parityAuthor) TableName() string { return "authors" }

type parityBook struct {
	Id          string                 `gorm:"column:id;primaryKey"`
	Title       string                 `gorm:"column:title;not null;index"`
	AuthorId    string                 `gorm:"column:author_id;not null;index"`
	Author      *parityAuthor          `gorm:"foreignKey:AuthorId"`
	Genre       parityGenre            `gorm:"column:genre"`
	Tags        []string               `gorm:"column:tags;serializer:json"`
	Attrs       map[string]string      `gorm:"column:attrs;serializer:json"`
	Meta        *parityMeta            `gorm:"column:meta;serializer:json"`
	Revisions   []*parityMeta          `gorm:"column:revisions;serializer:json"`
	SecretNote  string                 `gorm:"column:secret_note"`
	CacheHint   string                 `gorm:"-"`
	PublishedAt *timestamppb.Timestamp `gorm:"column:published_at;serializer:tspb"`
}

func (*parityBook) TableName() string { return "books" }

type parityReview struct {
	Id          int64  `gorm:"column:id;primaryKey;autoIncrement"`
	BookId      string `gorm:"column:book_id;not null;index"`
	AuthorEmail string `gorm:"column:author_email;index"`
	Body        string `gorm:"column:body"`
	Stars       int64  `gorm:"column:stars;not null;default:0"`
}

func (*parityReview) TableName() string { return "reviews" }

// Compiles the kitchen sink without the external test helpers
func parityModels(t *testing.T) []*Model {
	t.Helper()
	src, err := os.ReadFile("testdata/test.proto")
	if err != nil {
		t.Fatalf("read test proto: %v", err)
	}
	img, err := testproto.Image("test/v1/test.proto", map[string]string{
		"test/v1/test.proto": string(src),
	})
	if err != nil {
		t.Fatalf("build image: %v", err)
	}
	files, err := LoadImage(img)
	if err != nil {
		t.Fatalf("load image: %v", err)
	}
	models, err := Collect(files)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return models
}

var parityStructs = map[string]any{
	"Author": &parityAuthor{},
	"Book":   &parityBook{},
	"Review": &parityReview{},
}

// Hand struct tags must equal computed generator tags
func TestParityStructsMatchComputedTags(t *testing.T) {
	for _, m := range parityModels(t) {
		hand, ok := parityStructs[m.Name]
		if !ok {
			t.Fatalf("no parity struct for model %s", m.Name)
		}
		tags := computeTags(m)
		st := reflect.TypeOf(hand).Elem()
		for goName, want := range tags {
			field, ok := st.FieldByName(goName)
			if !ok {
				t.Errorf("%s missing field %s", m.Name, goName)
				continue
			}
			if got := field.Tag.Get("gorm"); got != want.gorm {
				t.Errorf("%s.%s tag %q, want %q", m.Name, goName, got, want.gorm)
			}
		}
		for i := 0; i < st.NumField(); i++ {
			f := st.Field(i)
			if _, ok := tags[f.Name]; !ok && f.Tag.Get("gorm") != "" {
				t.Errorf("%s.%s tagged but not generated", m.Name, f.Name)
			}
		}
	}
}

// Descriptor specs and gorm parse must see one schema
// Timestamp columns are the one designed divergence
func TestParitySpecAgreesWithGorm(t *testing.T) {
	models := parityModels(t)
	spec, err := BuildSpec(models, migrate.Dialects())
	if err != nil {
		t.Fatalf("build spec: %v", err)
	}

	dialectors := []gorm.Dialector{
		sqlite.Open("file::memory:"),
		postgres.Open(""),
	}
	cache := &sync.Map{}
	namer := schema.NamingStrategy{IdentifierMaxLength: 64}

	for _, m := range models {
		parsed, err := schema.Parse(parityStructs[m.Name], cache, namer)
		if err != nil {
			t.Fatalf("parse %s: %v", m.Name, err)
		}
		table := spec.Table(parsed.Table)
		if table == nil {
			t.Fatalf("spec missing table %s", parsed.Table)
		}

		columns := 0
		for _, field := range parsed.Fields {
			if field.DBName == "" {
				continue
			}
			columns++
			col := table.Column(field.DBName)
			if col == nil {
				t.Errorf("spec missing column %s.%s", parsed.Table, field.DBName)
				continue
			}
			if col.NotNull != (field.NotNull || field.PrimaryKey) {
				t.Errorf("%s.%s not null %v, gorm %v", parsed.Table, field.DBName, col.NotNull, field.NotNull)
			}
			if col.PK != field.PrimaryKey {
				t.Errorf("%s.%s pk %v, gorm %v", parsed.Table, field.DBName, col.PK, field.PrimaryKey)
			}
			if col.Unique != field.Unique {
				t.Errorf("%s.%s unique %v, gorm %v", parsed.Table, field.DBName, col.Unique, field.Unique)
			}
			gormDefault := ""
			if field.HasDefaultValue {
				gormDefault = field.DefaultValue
			}
			if col.Default != gormDefault {
				t.Errorf("%s.%s default %q, gorm %q", parsed.Table, field.DBName, col.Default, gormDefault)
			}

			for _, dialector := range dialectors {
				d, err := migrate.DialectByName(dialector.Name())
				if err != nil {
					t.Fatalf("dialect: %v", err)
				}
				specType, err := col.TypeFor(d.Name())
				if err != nil {
					t.Fatalf("%s.%s: %v", parsed.Table, field.DBName, err)
				}
				gormType := dialector.DataTypeOf(field)

				// Timestamps store as time, gorm parses them text
				if field.TagSettings["SERIALIZER"] == "tspb" {
					wantSpec := d.TypeOf(migrate.LogicalColumn{Kind: migrate.TypeTime})
					if specType != wantSpec {
						t.Errorf("%s.%s %s spec type %q, want %q", parsed.Table, field.DBName, d.Name(), specType, wantSpec)
					}
					if d.NormalizeType(gormType) != "text" {
						t.Errorf("%s.%s %s gorm type %q, divergence contract broken", parsed.Table, field.DBName, d.Name(), gormType)
					}
					continue
				}
				// Sqlite spells autoincrement inside the type
				gormType = strings.TrimSuffix(gormType, " PRIMARY KEY AUTOINCREMENT")
				if d.NormalizeType(specType) != d.NormalizeType(gormType) {
					t.Errorf("%s.%s %s spec type %q, gorm %q", parsed.Table, field.DBName, d.Name(), specType, gormType)
				}
			}
		}
		if columns != len(table.Columns) {
			t.Errorf("%s has %d spec columns, gorm sees %d", parsed.Table, len(table.Columns), columns)
		}

		indexes := parsed.ParseIndexes()
		if len(indexes) != len(table.Indexes) {
			t.Errorf("%s has %d spec indexes, gorm sees %d", parsed.Table, len(table.Indexes), len(indexes))
		}
		for _, idx := range indexes {
			specIdx := table.Index(idx.Name)
			if specIdx == nil {
				t.Errorf("spec missing index %s on %s", idx.Name, parsed.Table)
				continue
			}
			if specIdx.Unique != strings.EqualFold(idx.Class, "UNIQUE") {
				t.Errorf("index %s unique %v, gorm class %q", idx.Name, specIdx.Unique, idx.Class)
			}
			if specIdx.Where != idx.Where {
				t.Errorf("index %s where %q, gorm %q", idx.Name, specIdx.Where, idx.Where)
			}
			var gormCols []string
			for _, f := range idx.Fields {
				gormCols = append(gormCols, f.DBName)
			}
			if !reflect.DeepEqual(gormCols, specIdx.Columns) {
				t.Errorf("index %s columns %v, gorm %v", idx.Name, specIdx.Columns, gormCols)
			}
		}
	}
}
