package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/deanle/optivian-webhook/db"
	"github.com/deanle/optivian-webhook/handler"
	"github.com/deanle/optivian-webhook/models"
	"github.com/deanle/optivian-webhook/worker"
)

func main() {
	ctx := context.Background()

	database, err := db.Connect(ctx)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}

	if err := db.RunMigrations(database); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	store := db.NewStore(database)

	eventCh, pool, cancelWorkers := startWorkerPool(ctx, store)

	wh := handler.NewWebhookHandler(store, eventCh)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks", wh.Create)
	mux.HandleFunc("GET /webhooks/{id}", wh.Get)

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		log.Printf("listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	waitForShutdown(ctx, srv, eventCh, pool, cancelWorkers, database)
}

func waitForShutdown(
	ctx context.Context,
	srv *http.Server,
	eventCh chan *models.Event,
	pool *worker.Pool,
	cancelWorkers context.CancelFunc,
	database *sql.DB,
) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit
	log.Println("shutting down...")

	// 1. Stop accepting new HTTP requests.
	httpCtx, httpCancel := context.WithTimeout(ctx, 5*time.Second)
	defer httpCancel()
	if err := srv.Shutdown(httpCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}

	// 2. Close the channel so workers drain remaining buffered events and exit.
	close(eventCh)

	// 3. Wait up to 30s for workers to finish cleanly.
	workerDone := make(chan struct{})
	go func() {
		pool.Wait()
		close(workerDone)
	}()

	select {
	case <-workerDone:
		log.Println("workers finished")
	case <-time.After(30 * time.Second):
		log.Println("worker shutdown timed out — cancelling in-flight requests")
		cancelWorkers()
		pool.Wait()
	}

	database.Close()
	log.Println("shutdown complete")
}

func startWorkerPool(ctx context.Context, store db.EventStore) (chan *models.Event, *worker.Pool, context.CancelFunc) {
	concurrency := 5
	if v := os.Getenv("WORKER_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			concurrency = n
		}
	}

	eventCh := make(chan *models.Event, 100)
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	pool := worker.NewPool(store, eventCh, concurrency)
	pool.Start(workerCtx)
	return eventCh, pool, cancelWorkers
}
