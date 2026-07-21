// Compiles proto sources into descriptor images for tests
package testproto

import (
	"context"
	"fmt"

	"github.com/bufbuild/protocompile"
	"github.com/nickheyer/protogorm"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Compiles named sources plus the annotation schema into image bytes
func Image(entry string, sources map[string]string) ([]byte, error) {
	all := map[string]string{
		protogorm.OptionsPath: string(protogorm.OptionsProto),
	}
	for k, v := range sources {
		all[k] = v
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			Accessor: protocompile.SourceAccessorFromMap(all),
		}),
	}
	files, err := comp.Compile(context.Background(), entry)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}

	set := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	var add func(fd protoreflect.FileDescriptor)
	add = func(fd protoreflect.FileDescriptor) {
		if seen[fd.Path()] {
			return
		}
		seen[fd.Path()] = true
		for i := 0; i < fd.Imports().Len(); i++ {
			add(fd.Imports().Get(i).FileDescriptor)
		}
		set.File = append(set.File, protodesc.ToFileDescriptorProto(fd))
	}
	for _, fd := range files {
		add(fd)
	}
	return proto.Marshal(set)
}
