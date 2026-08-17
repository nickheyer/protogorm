// Reads a buf image and emits every gorm artifact
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/nickheyer/protogorm"
	"github.com/nickheyer/protogorm/internal/generator"
	"github.com/nickheyer/protogorm/migrate"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func main() {
	image := flag.String("image", "-", "buf descriptor image, - for stdin")
	options := flag.String("options", "", "proto root receiving the annotation schema")
	support := flag.String("support", "", "output root for per package gorm.gen.go files")
	store := flag.String("store", "", "store crud output as path:package")
	inject := flag.String("inject", "", "directory of pb.go files to tag")
	spec := flag.String("spec", "", "write the head schema snapshot to this path")
	migrations := flag.String("migrations", "", "migrations directory as dir:package")
	scaffold := flag.String("scaffold", "", "scaffold the next migration with this name")
	resolve := flag.String("resolve", "", "json file answering destructive diff demands")
	flag.Parse()

	if *options != "" {
		writeFile(filepath.Join(*options, filepath.FromSlash(protogorm.OptionsPath)), protogorm.OptionsProto)
	}
	if *support == "" && *store == "" && *inject == "" && *spec == "" && *scaffold == "" {
		if *options != "" {
			return
		}
		fatal("nothing to do, pass -options, -support, -store, -inject, -spec, or -scaffold")
	}
	if *scaffold != "" && *migrations == "" {
		fatal("-scaffold needs -migrations dir:package")
	}

	models, err := generator.Collect(readImage(*image))
	if err != nil {
		fatal("%v", err)
	}
	if len(models) == 0 {
		fatal("image has no messages with (protogorm.v1.model)")
	}

	if *support != "" {
		for _, pkg := range generator.ByPackage(models) {
			var own []*generator.Model
			for _, m := range models {
				if m.Pkg == pkg {
					own = append(own, m)
				}
			}
			data, err := generator.RenderSupport(pkg, own)
			if err != nil {
				fatal("%v", err)
			}
			writeFile(filepath.Join(*support, filepath.FromSlash(pkg.Dir), "gorm.gen.go"), data)
		}
	}

	if *store != "" {
		path, pkgName, ok := strings.Cut(*store, ":")
		if !ok {
			fatal("-store wants path:package, got %q", *store)
		}
		data, err := generator.RenderStore(models, pkgName)
		if err != nil {
			fatal("%v", err)
		}
		writeFile(path, data)
	}

	injected := 0
	if *inject != "" {
		n, err := generator.InjectDir(*inject, models)
		if err != nil {
			fatal("inject: %v", err)
		}
		injected = n
	}

	var head *migrate.Spec
	if *spec != "" || *scaffold != "" {
		head, err = generator.BuildSpec(models, migrate.Dialects())
		if err != nil {
			fatal("spec: %v", err)
		}
	}

	if *spec != "" {
		data, err := head.MarshalCanonical()
		if err != nil {
			fatal("spec: %v", err)
		}
		writeFile(*spec, data)
	}

	if *scaffold != "" {
		runScaffold(*migrations, *scaffold, *resolve, head)
	}

	fmt.Printf("protogorm: %d models, %d structs tagged\n", len(models), injected)
}

// Snapshot files carrying a chain position number
var snapshotPattern = regexp.MustCompile(`^(\d{4})_.*\.snapshot\.json$`)

// Diffs the last committed snapshot against head
// Writes the next migration pair into the chain directory
func runScaffold(migrations, name, resolve string, head *migrate.Spec) {
	dir, pkgName, ok := strings.Cut(migrations, ":")
	if !ok {
		fatal("-migrations wants dir:package, got %q", migrations)
	}

	from, ordinal := lastSnapshot(dir)
	if from == nil {
		fatal("no genesis or numbered snapshot in %s, fresh chains install at head without migrations", dir)
	}

	var res *migrate.Resolution
	if resolve != "" {
		data, err := os.ReadFile(resolve)
		if err != nil {
			fatal("read resolution: %v", err)
		}
		res = &migrate.Resolution{}
		if err := json.Unmarshal(data, res); err != nil {
			fatal("parse resolution: %v", err)
		}
	}

	files, demands, err := migrate.Scaffold(migrate.ScaffoldRequest{
		Package:    pkgName,
		Name:       name,
		Ordinal:    ordinal + 1,
		From:       from,
		Head:       head,
		Resolution: res,
	})
	if err != nil {
		fatal("scaffold: %v", err)
	}
	if len(demands) > 0 {
		fmt.Fprintln(os.Stderr, "protogorm: the diff needs decisions before it scaffolds")
		for _, d := range demands {
			fmt.Fprintf(os.Stderr, "  %s\n", d)
		}
		fmt.Fprintln(os.Stderr, "declare renames with was, drops with reserved names, or answer through -resolve")
		os.Exit(1)
	}

	if _, err := os.Stat(filepath.Join(dir, "registry.go")); os.IsNotExist(err) {
		bootstrap, err := migrate.RenderRegistryBootstrap(pkgName)
		if err != nil {
			fatal("bootstrap: %v", err)
		}
		files["registry.go"] = bootstrap
	}

	names := make([]string, 0, len(files))
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)
	for _, f := range names {
		writeFile(filepath.Join(dir, f), files[f])
		fmt.Printf("protogorm: wrote %s\n", filepath.Join(dir, f))
	}
}

// Finds the snapshot the chain currently ends on
// Returns the genesis at ordinal zero when chain is empty
func lastSnapshot(dir string) (*migrate.Spec, int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		fatal("read migrations dir: %v", err)
	}
	best := ""
	ordinal := 0
	for _, e := range entries {
		m := snapshotPattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= ordinal {
			continue
		}
		ordinal = n
		best = e.Name()
	}
	if best == "" {
		best = "genesis.snapshot.json"
		if _, err := os.Stat(filepath.Join(dir, best)); err != nil {
			return nil, 0
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, best))
	if err != nil {
		fatal("read snapshot: %v", err)
	}
	spec, err := migrate.ParseSpec(data)
	if err != nil {
		fatal("parse %s: %v", best, err)
	}
	return spec, ordinal
}

// Parses the image into a file descriptor sequence
func readImage(path string) iter.Seq[protoreflect.FileDescriptor] {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		fatal("read image: %v", err)
	}
	files, err := generator.LoadImage(data)
	if err != nil {
		fatal("%v", err)
	}
	return files
}

func writeFile(path string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		fatal("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		fatal("write %s: %v", path, err)
	}
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "protogorm: "+f+"\n", a...)
	os.Exit(1)
}
