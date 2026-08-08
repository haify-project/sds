package controller

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/event"
)

// NotifyManager owns the alert delivery channels that live in the database.
//
// Receivers from controller.toml are started once at boot and never change;
// these can be added, edited, muted and removed while the controller runs. That
// is the whole point of them: reconfiguring alerting by editing a file and
// restarting means a deliberate window with no alerting, on a cluster that is
// usually having a bad day already.
//
// Applying a change means stopping every channel's subscription and starting
// the current set. That is heavier than diffing, and it is chosen anyway: a
// subscription carries a queue of undelivered events, so a diff would have to
// decide what happens to the queue of a channel whose severity threshold just
// changed, and every answer to that is a surprise to somebody. Channel edits
// are a human-scale operation; a moment of resubscription costs nothing.
type NotifyManager struct {
	controller *Controller

	mu      sync.Mutex
	cancels []context.CancelFunc
	active  int
}

// NewNotifyManager creates the manager. It delivers nothing until Reload.
func NewNotifyManager(c *Controller) *NotifyManager {
	return &NotifyManager{controller: c}
}

// Active reports how many database-backed channels are currently subscribed.
func (nm *NotifyManager) Active() int {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	return nm.active
}

// Reload rebuilds every database-backed channel subscription from the current
// contents of the database.
//
// A channel whose configuration is unusable is skipped with a log line rather
// than failing the reload: one bad entry must not take down delivery to the
// others, which is the case where somebody is waiting for a page.
func (nm *NotifyManager) Reload(ctx context.Context) error {
	if nm.controller.db == nil || nm.controller.events == nil {
		return nil
	}
	channels, err := nm.controller.db.ListNotifyChannels(ctx)
	if err != nil {
		return fmt.Errorf("list notification channels: %w", err)
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()

	for _, cancel := range nm.cancels {
		cancel()
	}
	nm.cancels = nil
	nm.active = 0

	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		cfg, err := webhookConfigFor(ch)
		if err != nil {
			nm.controller.logger.Warn("Skipping unusable notification channel",
				zap.String("channel", ch.Name), zap.Error(err))
			continue
		}
		// Derived from the controller's own context, so shutdown still tears
		// every channel down without the manager being involved.
		cctx, cancel := context.WithCancel(nm.controller.ctx)
		event.NewWebhook(cfg, nm.controller.logger.With(zap.String("channel", ch.Name))).
			Start(cctx, nm.controller.events)
		nm.cancels = append(nm.cancels, cancel)
		nm.active++
	}

	nm.controller.logger.Info("Notification channels reloaded",
		zap.Int("enabled", nm.active), zap.Int("configured", len(channels)))
	return nil
}

// Test delivers one synthetic event to a single channel and reports what the
// far end said.
//
// It bypasses the bus on purpose. A test that went through the bus would be
// broadcast to every other channel too — paging everyone to check one bot URL —
// and it would return before delivery, so a wrong secret would show up as
// success here and as a log line nobody reads.
func (nm *NotifyManager) Test(ctx context.Context, name string) error {
	if nm.controller.db == nil {
		return fmt.Errorf("notification channels require the controller database")
	}
	ch, err := nm.controller.db.GetNotifyChannel(ctx, name)
	if err != nil {
		return err
	}
	cfg, err := webhookConfigFor(ch)
	if err != nil {
		return err
	}
	// Retries off: the caller is a human waiting on a button, and a failure is
	// the answer they asked for rather than something to paper over.
	cfg.Retries = 0

	return event.NewWebhook(cfg, nm.controller.logger).Deliver(ctx, event.Event{
		Type:     event.TypeResourceDegraded,
		Severity: event.SeverityInfo,
		Status:   event.StatusInfo,
		Message: fmt.Sprintf("Test message from SDS for channel %q. "+
			"Alerts about this cluster will arrive here.", ch.Name),
		Details:   map[string]string{"channel": ch.Name, "kind": ch.Kind},
		Timestamp: time.Now().UTC(),
	})
}

// webhookConfigFor turns a stored channel into a delivery configuration.
func webhookConfigFor(ch *database.NotifyChannel) (event.WebhookConfig, error) {
	kind, err := event.ParseKind(ch.Kind)
	if err != nil {
		return event.WebhookConfig{}, err
	}
	if err := validateChannelURL(ch.URL); err != nil {
		return event.WebhookConfig{}, err
	}
	// The URL's host settles the argument when it can. A Feishu bot URL with
	// the kind left on "generic" posts the raw event JSON, gets refused, and is
	// answered with HTTP 200 — indistinguishable from success anywhere in the
	// delivery path, because a generic receiver's body is the operator's own
	// and not ours to interpret. Caught here it costs one error message; missed
	// it costs an outage nobody hears about.
	if want, known := event.KindForURL(ch.URL); known && want != kind {
		return event.WebhookConfig{}, fmt.Errorf(
			"this URL is a %s endpoint but the channel kind is %q; %s will refuse every message "+
				"(and answer HTTP 200 while doing it). Set the kind to %s",
			want, kind, want, want)
	}
	types := make([]event.Type, 0, len(ch.Types))
	for _, t := range ch.Types {
		if t = strings.TrimSpace(t); t != "" {
			types = append(types, event.Type(t))
		}
	}
	return event.WebhookConfig{
		URL:     ch.URL,
		Kind:    kind,
		Secret:  ch.Secret,
		Headers: ch.Headers,
		Filter: event.Filter{
			MinSeverity: event.ParseSeverity(ch.MinSeverity),
			Types:       types,
		},
	}, nil
}

// validateChannelURL rejects a URL the delivery path could not use.
//
// The scheme check is not cosmetic: a value pasted with the scheme missing
// parses happily as a relative reference, and the resulting request fails per
// delivery with an error that describes the URL rather than the mistake.
func validateChannelURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("a notification channel needs a URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("notification URL is not a URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("notification URL must start with http:// or https:// (got %q)", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("notification URL has no host: %q", raw)
	}
	return nil
}
