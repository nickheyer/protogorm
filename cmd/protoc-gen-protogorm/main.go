// Protoc plugin emitting gorm support and store crud code
package main

import (
	"flag"
	"fmt"
	"iter"
	"path"

	"github.com/nickheyer/protogorm/internal/generator"
	"github.com/nickheyer/protogorm/migrate"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/pluginpb"
)

func main() {
	var flags flag.FlagSet
	store := flags.String("store", "", "emit store crud with this go package name")
	spec := flags.String("spec", "", "emit the head schema snapshot at this name")
	protogen.Options{ParamFunc: flags.Set}.Run(func(gen *protogen.Plugin) error {
		gen.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)

		models, err := generator.Collect(targetFiles(gen))
		if err != nil {
			return err
		}
		if len(models) == 0 {
			return fmt.Errorf("no messages with (protogorm.v1.model)")
		}

		if *spec != "" {
			head, err := generator.BuildSpec(models, migrate.Dialects())
			if err != nil {
				return err
			}
			data, err := head.MarshalCanonical()
			if err != nil {
				return err
			}
			g := gen.NewGeneratedFile(*spec, "")
			_, err = g.Write(data)
			return err
		}

		if *store != "" {
			data, err := generator.RenderStore(models, *store)
			if err != nil {
				return err
			}
			g := gen.NewGeneratedFile("store.gen.go", "")
			_, err = g.Write(data)
			return err
		}

		prefix := map[string]string{}
		for _, f := range gen.Files {
			if f.Generate {
				prefix[f.Desc.Path()] = f.GeneratedFilenamePrefix
			}
		}
		for _, pkg := range generator.ByPackage(models) {
			var own []*generator.Model
			for _, m := range models {
				if m.Pkg == pkg {
					own = append(own, m)
				}
			}
			data, err := generator.RenderSupport(pkg, own)
			if err != nil {
				return err
			}
			name := path.Join(path.Dir(prefix[own[0].Msg.ParentFile().Path()]), "gorm.gen.go")
			g := gen.NewGeneratedFile(name, protogen.GoImportPath(pkg.ImportPath))
			if _, err := g.Write(data); err != nil {
				return err
			}
		}
		return nil
	})
}

// Descriptors of the files buf asked us to generate
func targetFiles(gen *protogen.Plugin) iter.Seq[protoreflect.FileDescriptor] {
	return func(yield func(protoreflect.FileDescriptor) bool) {
		for _, f := range gen.Files {
			if f.Generate && !yield(f.Desc) {
				return
			}
		}
	}
}
