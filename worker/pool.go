package worker

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/deanle/optivian-webhook/db"
	"github.com/deanle/optivian-webhook/models"
)

const (
	maxAttempts    = 3
	defaultAPIURL  = "https://httpbin.org/delay/1"
)

type Pool struct {
	store       db.EventStore
	eventCh     <-chan *models.Event
	concurrency int
	apiURL      string
	wg          sync.WaitGroup
	httpClient  *http.Client
}

type Option func(*Pool)

// WithAPIURL overrides the external API endpoint. Useful in tests.
func WithAPIURL(url string) Option {
	return func(p *Pool) { p.apiURL = url }
}

func NewPool(store db.EventStore, eventCh <-chan *models.Event, concurrency int, opts ...Option) *Pool {
	p := &Pool{
		store:       store,
		eventCh:     eventCh,
		concurrency: concurrency,
		apiURL:      defaultAPIURL,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Start launches the worker goroutines. They exit when eventCh is closed
// or ctx is cancelled.
func (p *Pool) Start(ctx context.Context) {
	for range p.concurrency {
		p.wg.Add(1)
		go p.worker(ctx)
	}
}

// Wait blocks until all workers have exited.
func (p *Pool) Wait() {
	p.wg.Wait()
}

func (p *Pool) worker(ctx context.Context) {
	defer p.wg.Done()
	for event := range p.eventCh {
		p.process(ctx, event)
	}
}

func (p *Pool) process(ctx context.Context, event *models.Event) {
	if err := p.store.UpdateEventStatus(ctx, event.ID, models.StatusProcessing, nil); err != nil {
		log.Printf("event %s: mark processing: %v", event.ID, err)
		return
	}

	var lastErr error
	for attempt := range maxAttempts {
		if attempt > 0 {
			backoff := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
		}

		if err := p.callExternalAPI(ctx); err != nil {
			lastErr = err
			log.Printf("event %s: attempt %d/%d failed: %v", event.ID, attempt+1, maxAttempts, err)
			continue
		}

		processedAt := time.Now().UTC()
		if err := p.store.UpdateEventStatus(ctx, event.ID, models.StatusCompleted, &processedAt); err != nil {
			log.Printf("event %s: mark completed: %v", event.ID, err)
		}
		return
	}

	log.Printf("event %s: all %d attempts failed: %v", event.ID, maxAttempts, lastErr)
	processedAt := time.Now().UTC()
	if err := p.store.UpdateEventStatus(ctx, event.ID, models.StatusFailed, &processedAt); err != nil {
		log.Printf("event %s: mark failed: %v", event.ID, err)
	}
}

func (p *Pool) callExternalAPI(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.apiURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("external API returned %d", resp.StatusCode)
	}
	return nil
}
