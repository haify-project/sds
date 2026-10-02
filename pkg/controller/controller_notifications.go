package controller

import (
	"context"
	"encoding/json"
	"time"

	"github.com/haify-project/sds/pkg/alert"
	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/event"
)

// alertOptions assembles what the health detector is asked to watch.
//
// Split out of startNotifications so the wiring can be asserted directly. The
// observer in particular is the kind of connection that goes missing without
// anything failing: the detector keeps raising events, the metrics endpoint
// keeps answering, and only the numbers on it are quietly empty.
func (c *Controller) alertOptions() alert.Options {
	opts := alert.Options{
		Interval:     time.Duration(c.config.Alert.CheckIntervalSec) * time.Second,
		IdleInterval: time.Duration(c.config.Alert.IdleIntervalSec) * time.Second,
		Resources:    c.resources,
		Logger:       c.logger,
		WarningHold:  time.Duration(c.config.Alert.WarningHoldSec) * time.Second,
	}
	// Node reachability costs an SSH round trip per node per poll, so it is a
	// separate switch from the resource checks, which are served from state the
	// controller already gathers.
	if c.config.Alert.CheckNodes {
		opts.Nodes = c.nodes
	}
	if c.config.Alert.CheckPools {
		opts.Pools = c.storage
		opts.NearFullPercent = c.config.Alert.PoolNearFullPercent
		opts.FullPercent = c.config.Alert.PoolFullPercent
	}
	// One poll, two consumers. The state the detector gathers to raise events is
	// the same state a dashboard needs, and collecting it twice would double the
	// SSH round trips to every node while letting the alert and the panel
	// disagree about the same instant — the disagreement an operator notices
	// first and trusts least.
	if c.metrics != nil {
		opts.Observer = newMetricsObserver(c)
	}
	return opts
}

// startNotifications brings up the event bus, the health detector that feeds
// it, and any configured Webhook receivers.
//
// A Webhook is no longer required to enable this: the bus also backs the watch
// stream and the SSE endpoint, so an operator who wants to tail events without
// standing up an HTTP receiver just sets enabled = true.
func (c *Controller) startNotifications() {
	if !c.config.Alert.Enabled {
		// The health poll is the only thing that reads cluster state on a
		// schedule, so it is also the only source the storage and DRBD gauges
		// have. Disabling alerts silently empties half of /metrics, which is
		// precisely the "a flat zero looks like a healthy cluster" failure the
		// gauges were wired up to end — so say it once, out loud, at startup.
		if c.config.Metrics.Enabled {
			c.logger.Warn("Metrics are enabled but alerts are not; the pool, gateway and DRBD replication gauges stay empty because they are fed by the health poll. Set [alert] enabled = true to populate them.")
		}
		return
	}

	c.events = event.NewBus(c.config.Alert.HistorySize)
	c.persistEvents()

	opts := c.alertOptions()
	c.alertMonitor = alert.NewMonitor(c.events, opts)
	c.alertMonitor.Start(c.ctx)
	if c.config.Alert.WatchDRBDEvents {
		c.watchDRBDEvents(c.ctx, c.alertMonitor)
	}

	// Database-backed channels come up before the file-based ones so a UI-added
	// channel is delivering by the time the first poll finishes.
	c.notify = NewNotifyManager(c)
	if err := c.notify.Reload(c.ctx); err != nil {
		c.logger.Warn("Failed to load notification channels", zap.Error(err))
	}

	for _, wh := range c.config.Alert.Receivers() {
		event.NewWebhook(event.WebhookConfig{
			URL:      wh.URL,
			Headers:  wh.Headers,
			Filter:   event.Filter{MinSeverity: event.ParseSeverity(wh.MinSeverity)},
			OnResult: c.recordDelivery(receiverName(wh.URL)),
		}, c.logger).Start(c.ctx, c.events)
		c.logger.Info("Alert webhook registered",
			zap.String("url", wh.URL),
			zap.String("min_severity", wh.MinSeverity))
	}

	c.logger.Info("Notifications started",
		zap.Duration("interval", opts.Interval),
		zap.Bool("node_checks", opts.Nodes != nil),
		zap.Bool("feeding_metrics", opts.Observer != nil),
		zap.Int("webhooks", len(c.config.Alert.Receivers())),
		zap.Int("channels", c.notify.Active()))
}

// persistEvents keeps the event history in the database, so it survives the
// controller restarting — which under Self-HA is every failover, the moment an
// operator most wants to read what just happened.
func (c *Controller) persistEvents() {
	if c.db == nil {
		return
	}
	retention := c.config.Alert.HistorySize
	if retention <= 0 {
		retention = event.DefaultHistory
	}
	records, err := c.db.RecentEventRecords(c.ctx, retention)
	if err != nil {
		c.logger.Warn("Could not load the event history", zap.Error(err))
	}
	restored := make([]event.Event, 0, len(records))
	for _, r := range records {
		var e event.Event
		if json.Unmarshal(r, &e) == nil {
			restored = append(restored, e)
		}
	}
	c.events.Restore(restored)
	c.events.SetPersister(func(e event.Event) {
		data, err := json.Marshal(e)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.db.AppendEventRecord(ctx, data, retention); err != nil {
			c.logger.Warn("Could not record an event", zap.Error(err))
		}
	})
	if len(restored) > 0 {
		c.logger.Info("Event history restored", zap.Int("events", len(restored)))
	}
}
