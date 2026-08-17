# protogorm

A buf plugin for turning your wire type into a gorm schema with free CRUD and protoc interop.

protogorm reads `(protogorm.v1.model)` + `(protogorm.v1.db)` proto annotations and you get:

1. **Tags** - gorm struct tags injected into the structs protoc-gen-go already made. The `.pb.go` struct *is* the table. While this is more-so a cool feature of gorm, proto derived gorm tags is a game changer.
2. **Support** - `gorm.gen.go` beside your generated package: `TableName`, timestamp hooks, `Redact` (returns a clone with secret fields cleared, never mutates the row you loaded), `AllModels` in one slice, and a serializer that stores `google.protobuf.Timestamp` as a real time column.
3. **Store** - `store.gen.go` of typed CRUD methods (plus any annotated queries) on your `Store` type, in whatever package that lives.

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

A single string pk named `id` gets a uuid filled in on create when empty. Fields typed as another model become relations. Maps, lists, and message fields serialize to json columns. Timestamps get real time columns per dialect (`datetime` on sqlite, `timestamptz` on postgres).

Schema history lives in the proto too: rename a column and list the old name in `was`, rename a table and put the old name in the model's `was`, delete a field and `reserve` its name. The migration differ reads all of it and stops asking you questions.

```proto
message User {
  option (protogorm.v1.model) = {table: "users", was: ["accounts"]};
  reserved "legacy_flags";
  string display_name = 2 [(protogorm.v1.db) = {was: ["username"]}];
}
```

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

## Migrations

`github.com/nickheyer/protogorm/migrate` moves live dbs between schemas with the protos as the single source of truth. Basically atlas but better.

The parts:

- **Spec** - derived straight from the descriptors, no compiled structs needed. Every dialect's column spelling is rendered at gen time and committed as json, so the schema your build ships is a file you can read in review. A parity test locks the derivation against gorm's own schema parser so the two can never drift.
- **Fingerprint** - a normalized, column-order-independent hash of a schema as one dialect stores it (`SpecOfDB`).
- **Chain** - a `Registry` holds a genesis snapshot plus ordered `Migration` steps.
- **Engine** - on boot it proves where the database sits.
- **Scaffold** - diffs the last committed snapshot against the head spec and emits the next migration file pair, bootstrapping the migrations package on first run. Renames come from `was`, drops from `reserved` names, and anything still ambiguous is refused with a demand list instead of guessed.

The image-driven CLI does both:

```sh
buf build -o - | go run github.com/nickheyer/protogorm/cmd/protogorm \
    -spec internal/db/migrations/head.snapshot.json

buf build -o - | go run github.com/nickheyer/protogorm/cmd/protogorm \
    -migrations internal/db/migrations:migrations \
    -scaffold add_lobby_flags
```

Run `-spec` inside your normal gen step so the head snapshot always matches the protos. Run `-scaffold` when you change the schema; anything it cannot resolve from proto history it asks for through a `-resolve` json file. Fresh installs never replay the chain, the engine creates every table at head in one step and stamps the ledger.

## Testing

Generator output is locked by golden files under `internal/generator/testdata`.
After changing the generator, inspect the diff and refresh with:

```sh
go test ./internal/generator -run TestGolden -update
```

## Vendoring the options

Until the buf registry lets me register (?) my plugin, copy `proto/protogorm/v1/options.proto` into your buf module and exclude it from your own Go generation. Your runtime never imports it, only the generator reads it.
