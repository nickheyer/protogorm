// Locks generator output against committed goldens
package generator_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/nickheyer/protogorm/internal/generator"
	"github.com/nickheyer/protogorm/internal/testproto"
)

var update = flag.Bool("update", false, "rewrite golden files")

// Compiles the kitchen sink schema through the image path
func compileModels(t *testing.T) []*generator.Model {
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
	files, err := generator.LoadImage(img)
	if err != nil {
		t.Fatalf("load image: %v", err)
	}
	models, err := generator.Collect(files)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("collected %d models, want 3", len(models))
	}
	return models
}

func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, got, 0644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s, regenerate with -update", path)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("%s drifted from golden, inspect then rerun with -update", path)
	}
}

func TestGoldenSupport(t *testing.T) {
	models := compileModels(t)
	pkgs := generator.ByPackage(models)
	if len(pkgs) != 1 {
		t.Fatalf("got %d packages, want 1", len(pkgs))
	}
	data, err := generator.RenderSupport(pkgs[0], models)
	if err != nil {
		t.Fatalf("render support: %v", err)
	}
	checkGolden(t, filepath.Join("testdata", "golden", "gorm.gen.go.golden"), data)
}

func TestGoldenStore(t *testing.T) {
	models := compileModels(t)
	data, err := generator.RenderStore(models, "db")
	if err != nil {
		t.Fatalf("render store: %v", err)
	}
	checkGolden(t, filepath.Join("testdata", "golden", "store.gen.go.golden"), data)
}

func TestGoldenInject(t *testing.T) {
	models := compileModels(t)
	src, err := os.ReadFile(filepath.Join("testdata", "sample_pb.go.txt"))
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pb.go")
	if err := os.WriteFile(path, src, 0644); err != nil {
		t.Fatalf("write sample: %v", err)
	}

	n, err := generator.InjectDir(dir, models)
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if n != 3 {
		t.Errorf("tagged %d structs, want 3", n)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read injected: %v", err)
	}
	checkGolden(t, filepath.Join("testdata", "golden", "injected.go.golden"), got)

	if _, err := generator.InjectDir(dir, models); err != nil {
		t.Fatalf("second inject: %v", err)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read reinjected: %v", err)
	}
	if !bytes.Equal(got, again) {
		t.Error("inject is not idempotent")
	}
}
