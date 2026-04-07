# Webhook Processor

A Go HTTP service that receives webhook events, stores them in PostgreSQL, and processes them asynchronously via a worker pool.

## Setup

**Prerequisites:** Go 1.22+, Docker

```bash
# Start Postgres
docker-compose up -d

# Install dependencies
go mod tidy

# Run the server
go run ./cmd/server
```

## Environment Variables

| Variable             | Default                                                        | Description                    |
|----------------------|----------------------------------------------------------------|--------------------------------|
| `DATABASE_URL`       | `postgres://postgres:postgres@localhost:5432/webhooks?sslmode=disable` | Postgres connection string |
| `ADDR`               | `:8080`                                                        | HTTP listen address            |
| `WORKER_CONCURRENCY` | `5`                                                            | Number of worker goroutines    |

## API

### POST /webhooks
Create a new webhook event.

```bash
curl -X POST localhost:8080/webhooks \
  -H 'Content-Type: application/json' \
  -d '{"event_type":"order.created","source":"checkout","payload":{"order_id":42}}'
# {"id":"<uuid>"}
```

### GET /webhooks/:id
Fetch an event and its current processing status.

```bash
curl localhost:8080/webhooks/<uuid>
```

**Status values:** `pending` → `processing` → `completed` | `failed`

## Running Tests

**Unit tests** (no dependencies):
```bash
go test ./...
```

**Integration and E2E tests** require Postgres. Set `DATABASE_URL` and ensure the container is running:
```bash
docker-compose up -d
DATABASE_URL=postgres://postgres:postgres@localhost:5432/webhooks?sslmode=disable go test ./...
```

Tests in `db/` and `integration_test/` are automatically skipped when `DATABASE_URL` is not set.

## Design Decisions

- **Standard library router** — Go 1.22 added native method-based routing and `r.PathValue()`, so no external router needed.
- **No ORM** — raw parameterized SQL via `database/sql` + `lib/pq`. Explicit queries are easier to audit and optimize.
- **Buffered channel for backpressure** — events are published to a `chan *models.Event` (buffer 100). If the channel is full, the event remains in `pending` state in the DB and the HTTP response still succeeds.
- **`EventStore` interface** — both the handler and worker depend on an interface, not a concrete DB type. This makes unit testing straightforward with a mock store.
- **Drain-then-cancel shutdown** — on SIGTERM/SIGINT, the HTTP server stops accepting requests, then the event channel is closed so workers drain remaining buffered events naturally. A 30-second timeout guards against stalled workers; if exceeded, the worker context is cancelled to interrupt in-flight HTTP calls.

## What I'd Improve

- **Structured logging** — replace `log.Printf` with `slog` for JSON-formatted, level-aware logs.
- **Metrics** — expose a `/metrics` endpoint (Prometheus) for queue depth, processing latency, and error rates.
- **Migration versioning** — use a tool like `goose` or `golang-migrate` to manage schema versions rather than running raw SQL at startup.
- **Database-back queue instead of in-memory** - This would help solving 2 issues at the same time:
    - Events in the (in-memory) channel are lost on crash. A DB-backed queue (poll on startup for `processing` events) would help worker picks up the event where they were left off
    - If the channel is full, the event is stored in DB as pending and then silently dropped from the queue. It will sit in pending forever — no worker will ever pick it up again. The non-blocking send protects latency but creates a correctness hole. A recovery path is needed: a startup scan for stuck pending/processing events and re-enqueue them
- **Dual-write inconsistency** — after a successful external API call, a failure to update the DB status leaves the event stuck in `processing`. The outbox pattern (write intent and mark complete in the same DB transaction) would eliminate this gap.
- **Idempotency support** - Currently each POST /webhooks always creates a new event. There's no support for a caller-supplied idempotency key, so network retries by the webhook sender will create duplicate events.
- **No backoff jitter** - All workers retrying their respective failed events will sleep for the exact same duration before retrying. At scale with many workers, this causes synchronized retry thundering herd. Adding random time jitter to spread the load