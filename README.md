# protogorm

A buf plugin for turning your wire type into a gorm schema with free CRUD and protoc interop.

protogorm reads `(protogorm.v1.model)` + `(protogorm.v1.db)` proto annotations and you get:

1. **Tags** — gorm struct tags injected into the structs protoc-gen-go already made. The `.pb.go` struct *is* the table. While this is more-so a cool feature of gorm, proto derived gorm tags is a game changer.
2. **Support** — `gorm.gen.go` beside your generated package: `TableName`, timestamp hooks, `Redact` (returns a clone with secret fields cleared, never mutates the row you loaded), `AllModels` for automigrate, and a serializer that stores `google.protobuf.Timestamp` as a datetime column.
3. **Store** — `store.gen.go` of typed CRUD methods (plus any annotated queries) on your `Store` type, in whatever package that lives.

## Annotate

```proto
import "protogorm/v1/options.proto";

message User {
  option (protogorm.v1.model) = {
    table: "users"
    order_by: "created_at DESC"
    queries: [{
      name: "GetUserByEmail"
      kind: QUERY_KIND_GET
      where: "email = ?"
      params: ["email string"]
    }]
  };
  string id = 1 [(protogorm.v1.db) = {tag: "primaryKey"}];
  string email = 2 [(protogorm.v1.db) = {tag: "uniqueIndex"}];
  string hash = 3 [(protogorm.v1.db) = {redact: true}];
  google.protobuf.Timestamp created_at = 4 [(protogorm.v1.db) = {tag: "autoCreateTime"}];
}
```

A single string pk named `id` gets a uuid filled in on create when empty. Fields typed as another model become relations. Maps, lists, and message fields serialize to json columns. Timestamps become datetime columns.

## Run

After `buf generate`, feed it the image and tell it where things go:

```sh
buf build -o - | go run github.com/nickheyer/protogorm/cmd/protogorm \
    -support pkg/proto \
    -store internal/db/store.gen.go:db \
    -inject pkg/proto
```

`-support` mirrors protoc-gen-go `paths=source_relative` layout. `-store` wants `path:package` and expects a `Store` struct with a `db *gorm.DB` field in that package. `-inject` rewrites the tags in place, which is why this runs after buf and not inside it: a protoc plugin cannot touch another plugin's output.

Support and store generation also work as a plain protoc plugin for setups that prefer it:

```yaml
# buf.gen.yaml
  - local: protoc-gen-protogorm
    out: pkg/proto
    opt: [paths=source_relative]
  - local: protoc-gen-protogorm
    out: internal/db
    opt: [store=db]
```

Injection still runs after, via `protogorm -inject`.

## Runtime scrubbing

`protogorm.Scrub(msg)` walks any proto message tree and clears every populated
`redact: true` field, returning how many it caught. Wire it as an interceptor
backstop so a handler that forgets `Redact()` still cannot leak a secret:

```go
if n := protogorm.Scrub(resp.Any().(proto.Message)); n > 0 {
    log.Error("redact backstop caught %d secret fields on %s", n, procedure)
}
```

## Testing

Generator output is locked by golden files under `internal/generator/testdata`.
After changing the generator, inspect the diff and refresh with:

```sh
go test ./internal/generator -run TestGolden -update
```

## Vendoring the options

Until the buf registry lets me register (?) my plugin, copy `proto/protogorm/v1/options.proto` into your buf module and exclude it from your own Go generation. Your runtime never imports it, only the generator reads it.
