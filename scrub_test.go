// Verifies redact scrubbing across nested message trees
package protogorm_test

import (
	"os"
	"testing"

	"github.com/nickheyer/protogorm"
	"github.com/nickheyer/protogorm/internal/generator"
	"github.com/nickheyer/protogorm/internal/testproto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Builds dynamic message types from the kitchen sink schema
func testTypes(t *testing.T) (protoreflect.MessageDescriptor, protoreflect.MessageDescriptor) {
	t.Helper()
	src, err := os.ReadFile("internal/generator/testdata/test.proto")
	if err != nil {
		t.Fatalf("read test proto: %v", err)
	}
	img, err := testproto.Image("test/v1/test.proto", map[string]string{
		"test/v1/test.proto": string(src),
	})
	if err != nil {
		t.Fatalf("build image: %v", err)
	}
	files, err := generator.LoadImage(img)
	if err != nil {
		t.Fatalf("load image: %v", err)
	}
	var author, book protoreflect.MessageDescriptor
	for fd := range files {
		if fd.Path() != "test/v1/test.proto" {
			continue
		}
		msgs := fd.Messages()
		author = msgs.ByName("Author")
		book = msgs.ByName("Book")
	}
	if author == nil || book == nil {
		t.Fatal("test schema missing Author or Book")
	}
	return author, book
}

func TestScrubClearsSecrets(t *testing.T) {
	authorDesc, bookDesc := testTypes(t)

	author := dynamicpb.NewMessage(authorDesc)
	author.Set(authorDesc.Fields().ByName("id"), protoreflect.ValueOfString("a1"))
	author.Set(authorDesc.Fields().ByName("api_key"), protoreflect.ValueOfString("sekrit"))

	book := dynamicpb.NewMessage(bookDesc)
	book.Set(bookDesc.Fields().ByName("title"), protoreflect.ValueOfString("t"))
	book.Set(bookDesc.Fields().ByName("secret_note"), protoreflect.ValueOfString("hush"))
	book.Set(bookDesc.Fields().ByName("author"), protoreflect.ValueOfMessage(author))

	n := protogorm.Scrub(book)
	if n != 2 {
		t.Errorf("scrubbed %d fields, want 2", n)
	}
	if book.Has(bookDesc.Fields().ByName("secret_note")) {
		t.Error("secret_note survived scrub")
	}
	if author.Has(authorDesc.Fields().ByName("api_key")) {
		t.Error("nested api_key survived scrub")
	}
	if !book.Has(bookDesc.Fields().ByName("title")) {
		t.Error("title should survive scrub")
	}
	if got := book.Get(bookDesc.Fields().ByName("author")).Message().Get(authorDesc.Fields().ByName("id")).String(); got != "a1" {
		t.Errorf("author id = %q, want a1", got)
	}
}

func TestScrubRepeatedMessages(t *testing.T) {
	authorDesc, _ := testTypes(t)

	inner := dynamicpb.NewMessage(authorDesc)
	inner.Set(authorDesc.Fields().ByName("api_key"), protoreflect.ValueOfString("sekrit"))
	clean := dynamicpb.NewMessage(authorDesc)
	clean.Set(authorDesc.Fields().ByName("name"), protoreflect.ValueOfString("ok"))

	if n := protogorm.Scrub(inner); n != 1 {
		t.Errorf("scrubbed %d fields, want 1", n)
	}
	if n := protogorm.Scrub(clean); n != 0 {
		t.Errorf("clean message scrubbed %d fields, want 0", n)
	}
	if n := protogorm.Scrub(nil); n != 0 {
		t.Errorf("nil scrub returned %d, want 0", n)
	}
}
