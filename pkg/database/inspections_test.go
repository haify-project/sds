package database

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestInspectionsKeepNewestAndNumberThem(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()
	if _, recs, err := db.ListInspections(ctx, 0); err != nil || len(recs) != 0 {
		t.Fatalf("empty database: %v %v", recs, err)
	}
	for i := 1; i <= 4; i++ {
		id, err := db.AppendInspection(ctx, 3, func(id uint64) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"id":"%d"}`, id)), nil
		})
		if err != nil || id != uint64(i) {
			t.Fatalf("append %d: id %d, %v", i, id, err)
		}
	}
	ids, recs, err := db.ListInspections(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ids) != "[4 3 2]" || string(recs[0]) != `{"id":"4"}` {
		t.Errorf("want the newest three, newest first: %v", ids)
	}
	if _, err := db.GetInspection(ctx, 1); err == nil {
		t.Error("report 1 should have been dropped")
	}
	if raw, err := db.GetInspection(ctx, 3); err != nil || string(raw) != `{"id":"3"}` {
		t.Errorf("get 3: %s %v", raw, err)
	}
	if ids, _, _ := db.ListInspections(ctx, 1); fmt.Sprint(ids) != "[4]" {
		t.Errorf("limit: %v", ids)
	}
	// A build failure stores nothing.
	if _, err := db.AppendInspection(ctx, 3, func(uint64) ([]byte, error) { return nil, errors.New("boom") }); err == nil {
		t.Error("want the build error")
	}
	if ids, _, _ := db.ListInspections(ctx, 0); len(ids) != 3 {
		t.Errorf("after failed append: %v", ids)
	}
}

func TestNotifyDeliveryRecords(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()
	for i := uint64(1); i <= maxFailedEventIDs+5; i++ {
		if err := db.RecordNotifyDelivery(ctx, "ops", i, now, "HTTP 404"); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := db.ListNotifyDeliveries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := recs["ops"]
	if d.ConsecutiveFailures != maxFailedEventIDs+5 || d.LastError != "HTTP 404" || len(d.FailedEvents) != maxFailedEventIDs || d.FailedEvents[0] != 6 {
		t.Errorf("failures: %+v", d)
	}
	if err := db.RecordNotifyDelivery(ctx, "ops", 999, now, ""); err != nil {
		t.Fatal(err)
	}
	recs, _ = db.ListNotifyDeliveries(ctx)
	if d := recs["ops"]; d.ConsecutiveFailures != 0 || d.LastSuccessEvent != 999 || d.LastError != "HTTP 404" {
		t.Errorf("a success resets the streak and keeps the last error for reference: %+v", d)
	}
}
