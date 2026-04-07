# optivian-webhook

## What This Repo Does

Webhook ingestion service written in Go. It exposes two HTTP endpoints:

- **POST /webhooks** — accepts an event, persists it to Postgres with status `pending`, enqueues it on a buffered channel, and returns the event ID
- **GET /webhooks/{id}** — lets callers poll the processing status of a previously submitted event

An async worker pool drains the channel and processes each event by calling an external API (simulated via `GET https://httpbin.org/delay/1`). On success the status advances to `completed`; on failure after retries it becomes `failed`.

---

## Architecture

### Package Layout

| Package | Responsibility |
|---------|---------------|
| `models` | Shared types: `Event`, `EventStatus` constants (`pending`, `processing`, `completed`, `failed`) |
| `db` | `EventStore` interface + Postgres implementation; `Connect()` opens the pool and runs migrations |
| `handler` | HTTP handlers for POST and GET; depends on `EventStore` and the event channel |
| `worker` | Worker pool logic; depends on `EventStore` and the event channel |
| `cmd/server` | `main()` — wires everything together, owns lifecycle |

### Package Dependency Graph

```
cmd/server
  ├── db       (Connect, EventStore impl)
  ├── handler  (NewHandler)
  └── worker   (StartPool)
        └── db (EventStore interface)
handler
  └── db (EventStore interface)
models
  └── (no internal deps — imported by all packages)
```

### Data Flow

```
HTTP POST → handler → db.Create(pending) → channel (non-blocking send)
                                                     ↓
                                              worker goroutine
                                                     ↓
                                         db.UpdateStatus(processing)
                                                     ↓
                                       GET https://httpbin.org/delay/1
                                                     ↓
                                    db.UpdateStatus(completed | failed)

HTTP GET  → handler → db.GetByID → JSON response {id, status}
```

### EventStore Interface

Defined in `db`, consumed by both `handler` and `worker`. Enables injection of a mock in tests.

```go
type EventStore interface {
    Create(ctx context.Context, e *models.Event) error
    GetByID(ctx context.Context, id string) (*models.Event, error)
    UpdateStatus(ctx context.Context, id string, status models.EventStatus) error
}
```

### Worker Pool

- `StartPool(ctx, store, ch, concurrency)` launches `concurrency` goroutines (default 5, controlled by `WORKER_CONCURRENCY`)
- Each goroutine runs a `for event := range ch` loop — it blocks until the channel is closed or an item arrives
- A `sync.WaitGroup` tracks all goroutines; the caller waits on it during shutdown to drain in-flight work

### Retry Logic

Each event is attempted up to **3 times**. Between attempts:

```
backoff = 2^(attempt-1) seconds  →  0s, 1s, 2s
```

Sleep is context-aware (`select` on `time.After` and `ctx.Done()`), so shutdown cancels pending retries immediately.

### Graceful Shutdown Order

1. Receive OS signal (`SIGINT` / `SIGTERM`)
2. HTTP server shutdown with **5 s** timeout (drains in-flight HTTP requests)
3. Close the event channel (signals workers no new work is coming)
4. Wait for worker pool to drain with **30 s** timeout
5. Cancel the root context (unblocks any context-aware sleeps)
6. Close DB connection pool

### Buffered Channel

Created with capacity **100**. The POST handler uses a non-blocking send:

```go
select {
case ch <- event:
default:
    // channel full — event is already persisted as pending; drop silently
}
```

Events are never lost from the DB even if the channel is full; a future restart can re-enqueue them.

### DB Connection Pool

Configured in `db.Connect()`:

| Setting | Value |
|---------|-------|
| `SetMaxOpenConns` | 25 |
| `SetMaxIdleConns` | 5 |
| `SetConnMaxLifetime` | 5 min |

### Status Lifecycle

```
pending → processing → completed
                    ↘ failed
```

---

## API Surface

### POST /webhooks

Request body:
```json
{ "event_type": "...", "source": "...", "payload": {} }
```

Response `201`:
```json
{ "id": "<uuid>" }
```

Errors: `400` (bad request), `500` (DB error)

### GET /webhooks/{id}

Response `200`:
```json
{ "id": "<uuid>", "status": "pending|processing|completed|failed" }
```

Errors: `400` (missing id), `404` (not found), `500` (DB error)

---

## Environment Variables

| Variable | Default | Where read |
|----------|---------|------------|
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:5432/webhooks?sslmode=disable` | `db.Connect()` |
| `ADDR` | `:8080` | `main()` |
| `WORKER_CONCURRENCY` | `5` | `startWorkerPool()` |

---

## Migration

`migrations/001_create_events.sql` — creates the `events` table using `CREATE TABLE IF NOT EXISTS` (idempotent). Run automatically at startup inside `db.Connect()`.

---

## Testing Guidelines

### Test Layers

| Layer | Location | What it tests |
|-------|----------|---------------|
| Unit | `handler/`, `worker/` | Handler routing + worker processing logic in isolation |
| DB integration | `db/store_test.go` | Real Postgres queries (requires `DATABASE_URL`) |
| End-to-end | `integration_test/e2e_test.go` | Full stack wired together |

### Unit Tests

- **`mockStore`** — in-memory `EventStore` implementation protected by a mutex; defined alongside tests
- **External API mock** — `httptest.NewServer` serves a stub response; injected via a `WithAPIURL(url)` option on the worker/handler under test
- No real DB or network calls

### DB Integration Tests

- Require the `DATABASE_URL` environment variable to be set; skip automatically when absent:
  ```go
  if os.Getenv("DATABASE_URL") == "" {
      t.Skip("DATABASE_URL not set")
  }
  ```
- Test against a real Postgres instance (e.g., local Docker container)

### End-to-End Tests

- Wire the full stack: real DB, real worker pool, real HTTP server, mock external API (`httptest.NewServer`)
- Assert correctness by polling `GET /webhooks/{id}` until `status == "completed"` or timeout:
  - Max wait: **15 seconds**
  - Poll interval: **200 ms**

### Running Tests

```bash
# Unit tests only (no DATABASE_URL needed)
go test ./...

# All tests including DB integration and E2E
DATABASE_URL="postgres://postgres:postgres@localhost:5432/webhooks?sslmode=disable" go test ./...
```

### Assertion Libraries

| Library | Usage |
|---------|-------|
| `testify/assert` | Soft assertions — test continues on failure |
| `testify/require` | Fatal assertions — test stops on failure |
| `testify/mock` | Mock objects for handler-level tests |
