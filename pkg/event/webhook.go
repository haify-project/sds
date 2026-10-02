package event

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// WebhookConfig describes one HTTP receiver.
type WebhookConfig struct {
	// URL receives a POST per event.
	URL string
	// Kind is the message format the far end expects. Empty means generic: the
	// Event JSON unchanged. A chat service needs its own envelope and rejects
	// anything else — see render.go.
	Kind Kind
	// Secret is the signing secret for kinds that authenticate that way
	// (DingTalk's 加签). It is never logged and never leaves this process
	// except as a signature.
	Secret string
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
	// OnResult, when set, is told the final outcome of each bus-driven
	// delivery: nil once it was delivered, the last error once every attempt
	// failed. It runs on the delivery goroutine. A shutdown mid-retry reports
	// nothing, because nothing was decided.
	OnResult func(e Event, err error)
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

// Deliver sends one event now and reports what happened, instead of logging it
// and moving on the way the bus-driven path does.
//
// This is what a "send a test message" button needs. The background path is
// deliberately fire-and-forget — an operator must not be blocked on a chat
// service being slow — but a person who just entered a bot URL is owed the
// actual answer, including the far end's own rejection text.
func (w *Webhook) Deliver(ctx context.Context, e Event) error {
	body, err := w.cfg.Kind.Render(e)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	return w.post(ctx, body)
}

func (w *Webhook) deliver(ctx context.Context, e Event) {
	body, err := w.cfg.Kind.Render(e)
	if err != nil {
		w.log.Error("webhook: encode event", zap.Error(err))
		w.report(e, fmt.Errorf("encode event: %w", err))
		return
	}

	backoff := 500 * time.Millisecond
	var lastErr error
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
			lastErr = err
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
		w.report(e, nil)
		return
	}
	w.log.Error("webhook: giving up on event",
		zap.String("url", w.cfg.URL),
		zap.Uint64("event_id", e.ID),
		zap.String("message", e.Message))
	w.report(e, lastErr)
}

func (w *Webhook) report(e Event, err error) {
	if w.cfg.OnResult != nil {
		w.cfg.OnResult(e, err)
	}
}

func (w *Webhook) post(ctx context.Context, body []byte) error {
	// Signed per attempt, not once: DingTalk's signature covers a timestamp it
	// only accepts within an hour, so a retry after a long backoff needs a
	// fresh one.
	dest, err := w.cfg.Kind.SignedURL(w.cfg.URL, w.cfg.Secret, time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dest, bytes.NewReader(body))
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

	// The body has to be read even on a 200. Feishu, WeCom and DingTalk all
	// answer 200 for a message they refused, putting the reason in the body;
	// stopping at the status code would report every one of those as delivered.
	// Bounded because a misconfigured URL can point at anything at all.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return fmt.Errorf("read the receiver's reply: %w", err)
	}
	return w.cfg.Kind.CheckResponse(raw)
}
