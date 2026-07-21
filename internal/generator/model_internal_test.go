// Locks naming helpers against protoc-gen-go behavior
package generator

import "testing"

// Expected values mirror protoc-gen-go GoCamelCase output
func TestGoCamel(t *testing.T) {
	cases := map[string]string{
		"id":           "Id",
		"api_key":      "ApiKey",
		"mc_version":   "McVersion",
		"author_id":    "AuthorId",
		"field_1":      "Field_1",
		"field_1a":     "Field_1A",
		"a_b_c":        "ABC",
		"_hidden":      "XHidden",
		"http_server":  "HttpServer",
		"sha256_hash":  "Sha256Hash",
		"already_Up":   "Already_Up",
		"ApiToken":     "ApiToken",
		"double__key":  "Double_Key",
		"trailing_":    "Trailing_",
		"a1_b":         "A1B",
		"memory_mb":    "MemoryMb",
		"token_plaintext": "TokenPlaintext",
	}
	for in, want := range cases {
		if got := goCamel(in); got != want {
			t.Errorf("goCamel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParamName(t *testing.T) {
	cases := map[string]string{
		"id":        "id",
		"server_id": "serverID",
		"email":     "email",
		"use_count": "useCount",
	}
	for in, want := range cases {
		if got := paramName(in); got != want {
			t.Errorf("paramName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlural(t *testing.T) {
	cases := map[string]string{
		"Book":    "Books",
		"Session": "Sessions",
		"Status":  "Status",
	}
	for in, want := range cases {
		if got := plural(in); got != want {
			t.Errorf("plural(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanName(t *testing.T) {
	cases := map[string]string{
		"ApiToken":    "api token",
		"Book":        "book",
		"ServerProps": "server props",
	}
	for in, want := range cases {
		if got := humanName(in); got != want {
			t.Errorf("humanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripTagKey(t *testing.T) {
	raw := `protobuf:"bytes,1,opt,name=id" json:"id,omitempty" gorm:"column:id"`
	got := stripTagKey(raw, "gorm")
	want := `protobuf:"bytes,1,opt,name=id" json:"id,omitempty"`
	if got != want {
		t.Errorf("stripTagKey = %q, want %q", got, want)
	}
	if again := stripTagKey(got, "gorm"); again != want {
		t.Errorf("stripTagKey rerun = %q, want %q", again, want)
	}
}

func TestSplitTag(t *testing.T) {
	parts := splitTag(`protobuf:"bytes,1,opt,name=id" json:"id,omitempty" gorm:"column:id;primaryKey"`)
	if len(parts) != 3 {
		t.Fatalf("split into %d parts, want 3", len(parts))
	}
	if parts[2] != `gorm:"column:id;primaryKey"` {
		t.Errorf("last part = %q", parts[2])
	}
}
