// Ships the annotation schema inside the go module
package protogorm

import _ "embed"

// Import path consumers use for the schema
const OptionsPath = "protogorm/v1/options.proto"

// Source of the annotation schema
//
//go:embed proto/protogorm/v1/options.proto
var OptionsProto []byte
