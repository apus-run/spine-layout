# Spine service template

A Go 1.27.3 service template using Spine HTTP/gRPC transports, protobuf APIs,
Wire dependency injection, Ent, and MySQL. The Todo service demonstrates CRUD,
filtering, ordering, pagination, field-mask updates, snapshot streaming and
bidirectional synchronization.

The module path is `github.com/apus-run/spine-layout`. Spine is pinned to the post-v3.1.0 header-ownership fix in `go.mod`.
Its OpenTelemetry integration is pinned to v3.1.0. Both Spine
protobuf generators are pinned in `buf.gen.yaml` and run through Buf.

## Generate, build and test

```sh
make init
make all
make build
go test -race ./...
go vet ./...
```

`make all` regenerates API/error bindings, OpenAPI, configuration messages, Ent
and Wire, then tidies the module. Buf is pinned to v1.75.0. API generation uses
protoc-gen-go v1.36.11, protoc-gen-go-grpc v1.6.2 and the Spine HTTP/errors
plugins at v3.1.0. The error annotation schema in
`api/errors/errors.proto` comes from Spine; only the Todo API is generated.

Generated code belongs with its proto/schema sources. Regenerate after changing
the module path or imports when creating a service from this template.

## Run with MySQL

Create a development database and set its DSN through the Spine environment
prefix. Supply credentials through your environment or secret manager.

```sh
export SPINE_DATABASE_SOURCE='USER:PASSWORD@tcp(HOST:3306)/DATABASE?parseTime=true&loc=UTC&charset=utf8mb4'
go run ./cmd/server -conf ./configs
```

HTTP listens on port 8000 and gRPC on port 9000 by default. `SPINE_` settings
override legacy `KRATOS` settings. Database connection pool defaults are 10 idle
and 20 open connections, configurable through `max_idle_conns` and
`max_open_conns`. Timestamps use microsecond precision with MySQL DATETIME(6).

The supplied local config enables SQL debugging and automatic schema creation.
For production, set `debug` and `auto_migrate` to false and apply reviewed schema
migrations before starting the service.

## Business acceptance

Use a dedicated MySQL test database:

```sh
export SPINE_LAYOUT_TEST_DSN='USER:PASSWORD@tcp(HOST:3306)/TEST_DATABASE?parseTime=true&loc=UTC&charset=utf8mb4'
go test -tags=integration -race -count=1 -timeout=4m -v ./cmd/server
```

The test builds and starts the actual server executable with temporary config,
uses the generated HTTP and gRPC clients, and verifies:

- CRUD across HTTP/gRPC, UUIDv7 IDs and stable timestamps.
- Filtering, ordering, page tokens and field-mask updates, including clearing a
  boolean value and rejecting unknown/read-only paths.
- Generated error predicates for invalid requests and missing/deleted records.
- SSE snapshot events and WebSocket create/update/delete events with clean EOF.
- SQL soft-delete state, application shutdown and reads after process restart.
- Concurrent masked writers preserve each other's unselected fields.
- Eight concurrent readers for 30 seconds, recording request count and latency
  percentiles with zero failures required.

The database is migrated for the test and only records bearing the run's unique
prefix are removed. Ordinary unit tests use in-memory SQLite at the data boundary.
`WatchTodos` emits a snapshot page and then closes. `SyncTodos` processes client
changes until the send side closes.

CI repeats generation and checks for drift, runs build/race/vet checks, executes
the MySQL acceptance test, and scans reachable vulnerabilities.

## Create a project

After copying the template, update `go.mod`, proto `go_package` values, source
imports, the command directory and `cmd/server/main.go` service name, then run
`make all`. Follow `AGENTS.md` for the DTO/service, DO/biz and PO/data boundaries.

## Container

```sh
docker build -t your-service .
docker run --rm -p 8000:8000 -p 9000:9000 \
  -e SPINE_DATABASE_SOURCE \
  -v /path/to/configs:/data/conf:ro your-service
```
