package deployment

import (
	"context"
	"testing"
	"time"
)

func TestStreamLocalDeliversLinesAndStopsOnCancel(t *testing.T) {
	var got []string
	err := streamLocal(context.Background(), "printf 'a\\nb\\r\\n'", func(l string) { got = append(got, l) })
	if err == nil || len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %q, err %v; want both lines and an error saying the stream ended", got, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	first := make(chan struct{}, 1)
	go func() {
		done <- streamLocal(ctx, "echo up; exec sleep 600", func(string) { first <- struct{}{} })
	}()
	<-first
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stream that never ends must stop when its context does")
	}
}
