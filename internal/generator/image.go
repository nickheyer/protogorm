// Parses buf descriptor images into file descriptors
package generator

import (
	"fmt"
	"iter"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Decodes image bytes into a file descriptor sequence
func LoadImage(data []byte) (iter.Seq[protoreflect.FileDescriptor], error) {
	var fds descriptorpb.FileDescriptorSet
	if err := (proto.UnmarshalOptions{Resolver: protoregistry.GlobalTypes}).Unmarshal(data, &fds); err != nil {
		return nil, fmt.Errorf("unmarshal image: %w", err)
	}
	files, err := protodesc.NewFiles(&fds)
	if err != nil {
		return nil, fmt.Errorf("build files: %w", err)
	}
	return func(yield func(protoreflect.FileDescriptor) bool) {
		files.RangeFiles(yield)
	}, nil
}
