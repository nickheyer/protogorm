// Runtime backstop clearing redact fields from outbound messages
package protogorm

import (
	protogormv1 "github.com/nickheyer/protogorm/gen/protogorm/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Clears every populated redact field in the message tree
func Scrub(m proto.Message) int {
	if m == nil {
		return 0
	}
	return scrub(m.ProtoReflect())
}

func scrub(m protoreflect.Message) int {
	n := 0
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if redacted(fd) {
			m.Clear(fd)
			n++
			return true
		}
		switch {
		case fd.IsMap():
			if fd.MapValue().Kind() == protoreflect.MessageKind {
				v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
					n += scrub(mv.Message())
					return true
				})
			}
		case fd.IsList():
			if fd.Kind() == protoreflect.MessageKind {
				for i, list := 0, v.List(); i < list.Len(); i++ {
					n += scrub(list.Get(i).Message())
				}
			}
		case fd.Kind() == protoreflect.MessageKind:
			n += scrub(v.Message())
		}
		return true
	})
	return n
}

// Reads the redact flag off one field descriptor
func redacted(fd protoreflect.FieldDescriptor) bool {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || opts == nil {
		return false
	}
	ext, ok := proto.GetExtension(opts, protogormv1.E_Db).(*protogormv1.Field)
	return ok && ext.GetRedact()
}
