package database

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestEventRecordsKeepTheNewestOldestFirst(t *testing.T) {
	db, err := Open(&Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		if err := db.AppendEventRecord(ctx, []byte(fmt.Sprintf(`{"id":%d}`, i)), 100); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.RecentEventRecords(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || string(got[0]) != `{"id":3}` || string(got[2]) != `{"id":5}` {
		t.Fatalf("got %q, want the three newest, oldest first", got)
	}
}
