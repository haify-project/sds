package deployment

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/liliang-cn/dispatch/pkg/dispatch"
)

func TestCopyWithinGivesUpOnAStalledCopy(t *testing.T) {
	stall := make(chan struct{})
	defer close(stall)
	start := time.Now()
	_, err := copyWithin(context.Background(), 50*time.Millisecond, func() (*dispatch.CopyResult, error) {
		<-stall
		return nil, nil
	})
	if err == nil {
		t.Fatal("a copy that never returns must be reported as failed")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("gave up after %s, want about 50ms", time.Since(start))
	}

	res, err := copyWithin(context.Background(), time.Second, func() (*dispatch.CopyResult, error) {
		return &dispatch.CopyResult{}, nil
	})
	if err != nil || res == nil {
		t.Fatalf("a copy that returns is passed through, got %v %v", res, err)
	}
}

// A dispatch config without an [exec] section left dispatch's Copy with a
// zero-sized worker semaphore, so every copy blocked forever. The client must
// supply the parallelism itself.
func TestCopyDoesNotDeadlockWithoutExecParallelInTheConfig(t *testing.T) {
	// An SSH "server" that hangs up at once: the copy fails fast, so the
	// only way for it not to return is the semaphore.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	dir := t.TempDir()
	cfg := dir + "/config.toml"
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf(
		"[ssh]\nuser = \"root\"\nport = %d\ntimeout = \"2s\"\n\n[hosts.\"127.0.0.1\"]\naddresses = [\"127.0.0.1\"]\n", port)), 0600); err != nil {
		t.Fatal(err)
	}
	src := dir + "/f"
	if err := os.WriteFile(src, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := NewWithOptions(zap.NewNop(), Options{ConfigPath: cfg})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = c.dispatch.Copy(context.Background(), []string{"127.0.0.1"}, src, "f")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("dispatch Copy never returned: its worker semaphore has no capacity")
	}
}
