package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/haify-project/sds/pkg/config"
	"github.com/haify-project/sds/pkg/event"
	"github.com/haify-project/sds/pkg/inspect"
)

// ErrInspectionRunning is returned when a run is asked for while one is in
// progress. Two runs would probe every node twice and store two reports of
// the same moment.
var ErrInspectionRunning = errors.New("an inspection is already running; wait for it, then read it with `sds inspect show latest`")

// inspectionTimeout bounds one run. The probe round is bounded on its own;
// this covers the database reads around it.
const inspectionTimeout = 5 * time.Minute

// inspectionProbeTimeout bounds the one SSH round to every node.
const inspectionProbeTimeout = 90 * time.Second

// headlineItems is how many findings the inspection.completed event names.
const headlineItems = 5

// InspectionManager runs cluster inspections and stores their reports.
type InspectionManager struct {
	controller *Controller
	running    atomic.Bool
	now        func() time.Time
}

// NewInspectionManager creates the manager.
func NewInspectionManager(c *Controller) *InspectionManager {
	return &InspectionManager{controller: c, now: time.Now}
}

func (im *InspectionManager) cfg() config.InspectConfig {
	if im.controller.config == nil {
		return config.InspectConfig{}
	}
	return im.controller.config.Inspect
}

// Run inspects the cluster now, stores the report and publishes its summary.
// areas narrows the checks; empty inspects everything.
func (im *InspectionManager) Run(ctx context.Context, trigger inspect.Trigger, areas []inspect.Area) (*inspect.Report, error) {
	c := im.controller
	if c.db == nil {
		return nil, fmt.Errorf("inspection reports need the controller database")
	}
	for _, a := range areas {
		if !inspect.ValidArea(string(a)) {
			return nil, fmt.Errorf("unknown area %q (want one of %s)", a, areaList())
		}
	}
	if !im.running.CompareAndSwap(false, true) {
		return nil, ErrInspectionRunning
	}
	defer im.running.Store(false)

	ctx, cancel := context.WithTimeout(ctx, inspectionTimeout)
	defer cancel()

	report := &inspect.Report{Trigger: trigger, StartedAt: im.now(), Areas: areas}
	in := im.gather(ctx)
	report.Checks = inspect.Run(in, areas)
	report.Summary = inspect.Summarize(report.Checks)
	report.Pools = inspect.PoolSamples(in)
	report.FinishedAt = im.now()

	keep := im.cfg().Keep
	if keep <= 0 {
		keep = config.DefaultInspectKeep
	}
	if _, err := c.db.AppendInspection(ctx, keep, func(id uint64) ([]byte, error) {
		report.ID = strconv.FormatUint(id, 10)
		return json.Marshal(report)
	}); err != nil {
		return report, fmt.Errorf("store the inspection report: %w", err)
	}
	c.logger.Info("Inspection finished",
		zap.String("id", report.ID), zap.String("trigger", string(trigger)),
		zap.Int("fail", report.Summary.Fail), zap.Int("warn", report.Summary.Warn),
		zap.Int("error", report.Summary.Error), zap.Int("pass", report.Summary.Pass),
		zap.Duration("took", report.FinishedAt.Sub(report.StartedAt)))
	im.publish(report)
	return report, nil
}

// publish puts one inspection.completed event on the bus when the run's
// worst finding reaches notify_min, so it is delivered like any alert.
func (im *InspectionManager) publish(r *inspect.Report) {
	bus := im.controller.events
	if bus == nil {
		return
	}
	min := inspect.StatusWarn
	if v := strings.TrimSpace(im.cfg().NotifyMin); v != "" {
		min = inspect.ParseStatus(v)
	}
	worst := inspect.Worst(r.Checks)
	if !worst.AtLeast(min) {
		return
	}
	sev := event.SeverityInfo
	switch worst {
	case inspect.StatusFail:
		sev = event.SeverityCritical
	case inspect.StatusWarn, inspect.StatusError:
		sev = event.SeverityWarning
	}
	bus.Publish(event.Event{
		Type:     event.TypeInspectionCompleted,
		Severity: sev,
		Status:   event.StatusInfo,
		Message:  inspect.Headline(r, headlineItems),
		Details: map[string]string{
			"report": r.ID, "trigger": string(r.Trigger),
			"fail": strconv.Itoa(r.Summary.Fail), "warn": strconv.Itoa(r.Summary.Warn),
			"error": strconv.Itoa(r.Summary.Error), "pass": strconv.Itoa(r.Summary.Pass),
		},
	})
}

// Get returns a stored report; id "" or "latest" is the newest.
func (im *InspectionManager) Get(ctx context.Context, id string) (*inspect.Report, error) {
	db := im.controller.db
	if db == nil {
		return nil, fmt.Errorf("inspection reports need the controller database")
	}
	var raw []byte
	if id == "" || id == "latest" {
		_, records, err := db.ListInspections(ctx, 1)
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			return nil, fmt.Errorf("no inspection has run yet; start one with `sds inspect run`")
		}
		raw = records[0]
	} else {
		n, err := strconv.ParseUint(id, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("inspection id %q is not a number or \"latest\"", id)
		}
		if raw, err = db.GetInspection(ctx, n); err != nil {
			return nil, err
		}
	}
	var r inspect.Report
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("stored inspection is unreadable: %w", err)
	}
	return &r, nil
}

// List returns up to limit stored reports, newest first.
func (im *InspectionManager) List(ctx context.Context, limit int) ([]*inspect.Report, error) {
	db := im.controller.db
	if db == nil {
		return nil, fmt.Errorf("inspection reports need the controller database")
	}
	_, records, err := db.ListInspections(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*inspect.Report, 0, len(records))
	for _, raw := range records {
		var r inspect.Report
		if json.Unmarshal(raw, &r) == nil {
			out = append(out, &r)
		}
	}
	return out, nil
}

// inspectSchedule is the cron spec the scheduler adds, or "" when off.
func (sm *ScheduleManager) inspectSchedule() string {
	c := sm.controller
	if c.config == nil || !c.config.Inspect.Enabled || c.inspections == nil {
		return ""
	}
	return strings.TrimSpace(c.config.Inspect.Schedule)
}

// runInspectionTick is the cron entry for the scheduled inspection.
func (sm *ScheduleManager) runInspectionTick() {
	if _, err := sm.controller.inspections.Run(context.Background(), inspect.TriggerSchedule, nil); err != nil {
		sm.controller.logger.Warn("Scheduled inspection failed", zap.Error(err))
	}
}

func areaList() string {
	names := make([]string, len(inspect.Areas))
	for i, a := range inspect.Areas {
		names[i] = string(a)
	}
	return strings.Join(names, ", ")
}
