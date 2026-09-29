package deployment

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/liliang-cn/dispatch/pkg/inventory"
	dispatchssh "github.com/liliang-cn/dispatch/pkg/ssh"
	"golang.org/x/crypto/ssh"
)

// streamKeepalive is how often a stream's SSH connection is probed. A node
// that loses power sends no FIN; without a probe a read on its stream blocks
// until TCP gives up, which takes the better part of a quarter of an hour.
var streamKeepalive = 15 * time.Second

// StreamLines runs cmd on host and calls onLine with each line of its output
// as it arrives, until the command ends, the connection is lost or ctx is
// cancelled. It is for commands that do not end on their own, such as
// `drbdsetup events2`; dispatch's own streaming waits for the command to exit
// before it will close, so it cannot be stopped.
//
// Closing the connection does not stop a command started without a terminal:
// it runs on until it next writes. Callers bound such a command themselves,
// with timeout(1).
func (c *Client) StreamLines(ctx context.Context, host, cmd string, onLine func(string)) error {
	if isLocalAddress(host) {
		return streamLocal(ctx, cmd, onLine)
	}
	inv, err := inventory.New(c.configPath)
	if err != nil {
		return fmt.Errorf("load dispatch config: %w", err)
	}
	hosts, err := inv.GetHosts([]string{host})
	if err != nil || len(hosts) == 0 {
		return fmt.Errorf("resolve %s: %v", host, err)
	}
	h := hosts[0]
	sshCfg := inv.GetConfig().SSH
	var opts []dispatchssh.ClientOption
	if sshCfg.KnownHostsPath != "" {
		opts = append(opts, dispatchssh.WithKnownHosts(sshCfg.KnownHostsPath))
	}
	if sshCfg.StrictHostKey {
		opts = append(opts, dispatchssh.WithStrictHostKey(true))
	}
	client, err := dispatchssh.NewClient(sshCfg.KeyPath, opts...)
	if err != nil {
		return fmt.Errorf("ssh client: %w", err)
	}
	conn, err := client.Connect(dispatchssh.HostSpec{
		Address: h.Address, User: h.User, Port: h.Port, KeyPath: h.KeyPath,
		UserSet: h.UserSet, PortSet: h.PortSet, KeyPathSet: h.KeyPathSet,
	})
	if err != nil {
		return fmt.Errorf("connect %s: %w", host, err)
	}
	var closeOnce sync.Once
	closeConn := func() { closeOnce.Do(func() { _ = conn.Close() }) }
	defer closeConn()

	session, err := conn.NewSession()
	if err != nil {
		return fmt.Errorf("session on %s: %w", host, err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return err
	}
	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("start on %s: %w", host, err)
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(streamKeepalive)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				closeConn()
				return
			case <-t.C:
				if !keepalive(conn) {
					closeConn()
					return
				}
			}
		}
	}()

	err = scanLines(stdout, onLine)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("stream from %s: %w", host, err)
	}
	return fmt.Errorf("stream from %s ended", host)
}

// keepalive reports whether the connection still answers, within one
// keepalive period.
func keepalive(conn *ssh.Client) bool {
	answered := make(chan bool, 1)
	go func() {
		_, _, err := conn.SendRequest("keepalive@openssh.com", true, nil)
		answered <- err == nil
	}()
	select {
	case ok := <-answered:
		return ok
	case <-time.After(streamKeepalive):
		return false
	}
}

func streamLocal(ctx context.Context, cmd string, onLine func(string)) error {
	c := exec.CommandContext(ctx, "/bin/sh", "-c", cmd)
	stdout, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	if err := c.Start(); err != nil {
		return err
	}
	err = scanLines(stdout, onLine)
	_ = c.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("local stream ended")
}

func scanLines(r io.Reader, onLine func(string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		onLine(strings.TrimRight(sc.Text(), "\r"))
	}
	return sc.Err()
}

// isLocalAddress reports whether host names this machine, which dispatch
// runs without SSH and so may not have a key for.
func isLocalAddress(host string) bool {
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}
