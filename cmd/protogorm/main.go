// Reads a buf image and emits every gorm artifact
package main

import (
	"flag"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strings"

	"github.com/nickheyer/protogorm"
	"github.com/nickheyer/protogorm/internal/generator"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func main() {
	image := flag.String("image", "-", "buf descriptor image, - for stdin")
	options := flag.String("options", "", "proto root receiving the annotation schema")
	support := flag.String("support", "", "output root for per package gorm.gen.go files")
	store := flag.String("store", "", "store crud output as path:package")
	inject := flag.String("inject", "", "directory of pb.go files to tag")
	flag.Parse()

	if *options != "" {
		writeFile(filepath.Join(*options, filepath.FromSlash(protogorm.OptionsPath)), protogorm.OptionsProto)
	}
	if *support == "" && *store == "" && *inject == "" {
		if *options != "" {
			return
		}
		fatal("nothing to do, pass -options, -support, -store, or -inject")
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

	fmt.Printf("protogorm: %d models, %d structs tagged\n", len(models), injected)
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
