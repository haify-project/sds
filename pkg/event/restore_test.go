package event

import (
	"encoding/json"
	"testing"
)

// A controller that restarts — every Self-HA failover — must come back with
// the history the previous one saw, numbering on from where it left off.
func TestHistorySurvivesARestartThroughThePersister(t *testing.T) {
	var stored [][]byte
	first := NewBus(10)
	first.SetPersister(func(e Event) {
		b, _ := json.Marshal(e)
		stored = append(stored, b)
	})
	first.Publish(Event{Type: TypeNodeUnreachable, Node: "sdt2", Message: "node sdt2 is unreachable"})
	first.Publish(Event{Type: TypeResourceFailover, Resource: "r2", Message: "r2 failed over"})

	var restored []Event
	for _, b := range stored {
		var e Event
		if err := json.Unmarshal(b, &e); err != nil {
			t.Fatal(err)
		}
		restored = append(restored, e)
	}
	second := NewBus(10)
	second.Restore(restored)

	got := second.Recent(Filter{}, 0, 0)
	if len(got) != 2 || got[1].Resource != "r2" || got[0].Node != "sdt2" {
		t.Fatalf("history after restart: %+v", got)
	}
	if e := second.Publish(Event{Type: TypeResourceFailover}); e.ID != 3 {
		t.Errorf("numbering restarted at %d; a client resuming with since=2 would miss it", e.ID)
	}
}
