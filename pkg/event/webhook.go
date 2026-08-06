package event

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// WebhookConfig describes one HTTP receiver.
type WebhookConfig struct {
	// URL receives a POST per event with the Event JSON as the body.
	URL string
	// Filter narrows what this receiver is sent. A pager wants critical only; a
	// chat channel may want everything.
	Filter Filter
	// Headers are added to every request — an auth token, a routing key, the
	// content type expected by a particular chat service.
	Headers map[string]string
	// Timeout bounds a single delivery attempt. Zero uses 10s.
	Timeout time.Duration
	// Retries is how many additional attempts a failed delivery gets. Zero uses
	// 2 (three attempts in total).
	Retries int
}

// Webhook delivers bus events to an HTTP endpoint.
//
// Deliveries run on the Webhook's own goroutine, reading from its bus
// subscription, so a receiver that is slow or down delays only its own queue.
// A failed delivery is retried with backoff and then dropped: holding it would
// mean an unbounded queue, and a stale alert delivered ten minutes late is
// worse than no alert, because the operator acts on it as if it were current.
type Webhook struct {
	cfg    WebhookConfig
	client *http.Client
	log    *zap.Logger
	done   chan struct{}
}

// NewWebhook creates a receiver. It does not start delivering until Start.
func NewWebhook(cfg WebhookConfig, log *zap.Logger) *Webhook {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Retries <= 0 {
		cfg.Retries = 2
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Webhook{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
		log:    log,
		done:   make(chan struct{}),
	}
}

// Start subscribes to bus and delivers matching events until ctx is cancelled.
func (w *Webhook) Start(ctx context.Context, bus *Bus) {
	ch, cancel := bus.Subscribe(w.cfg.Filter)
	go func() {
		defer close(w.done)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-ch:
				if !ok {
					return
				}
				w.deliver(ctx, e)
			}
		}
	}()
}

// Wait blocks until the delivery goroutine has stopped. Only meaningful after
// Start and a cancelled context; used by tests to avoid racing on shutdown.
func (w *Webhook) Wait() { <-w.done }

func (w *Webhook) deliver(ctx context.Context, e Event) {
	body, err := json.Marshal(e)
	if err != nil {
		w.log.Error("webhook: encode event", zap.Error(err))
		return
	}

	backoff := 500 * time.Millisecond
	for attempt := 0; attempt <= w.cfg.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
		}
		if err := w.post(ctx, body); err != nil {
			w.log.Warn("webhook: delivery failed",
				zap.String("url", w.cfg.URL),
				zap.Uint64("event_id", e.ID),
				zap.Int("attempt", attempt+1),
				zap.Error(err))
			continue
		}
		w.log.Info("webhook: delivered",
			zap.String("url", w.cfg.URL),
			zap.String("type", string(e.Type)),
			zap.String("severity", string(e.Severity)),
			zap.String("resource", e.Resource))
		return
	}
	w.log.Error("webhook: giving up on event",
		zap.String("url", w.cfg.URL),
		zap.Uint64("event_id", e.ID),
		zap.String("message", e.Message))
}

func (w *Webhook) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range w.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	// 4xx other than 408/429 will not succeed on retry either, but reporting
	// them as errors is still right: the caller logs and moves on, and the
	// operator needs to see that their receiver is rejecting us.
	if resp.StatusCode >= 400 {
		return fmt.Errorf("receiver returned HTTP %d", resp.StatusCode)
	}
	return nil
}
