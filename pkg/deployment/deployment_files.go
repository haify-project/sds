package deployment

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/liliang-cn/dispatch/pkg/dispatch"
	"go.uber.org/zap"
)

// ============ Config Distribution ============

// DistributeConfig distributes a configuration file to multiple nodes
func (c *Client) DistributeConfig(ctx context.Context, hosts []string, content, remotePath string, opts ...ConfigOption) (*ConfigResult, error) {
	options := &configOptions{}
	for _, opt := range opts {
		opt(options)
	}

	c.logger.Info("Distributing config",
		zap.Strings("hosts", hosts),
		zap.String("path", remotePath))

	localTempFile := "/tmp/" + filepath.Base(remotePath) + ".tmp"

	configResult := &ConfigResult{
		Path:    remotePath,
		Success: true,
		Hosts:   make(map[string]*HostResult),
	}

	// Separate local and remote hosts
	var localHosts []string
	var remoteHosts []string
	localAddrs := getLocalIPs()
	for _, host := range hosts {
		if isLocalIP(host, localAddrs) {
			localHosts = append(localHosts, host)
		} else {
			remoteHosts = append(remoteHosts, host)
		}
	}

	// Handle local hosts - write directly
	for _, host := range localHosts {
		// Write to temp path
		if err := os.WriteFile(localTempFile, []byte(content), 0644); err != nil {
			c.logger.Error("Failed to write local config", zap.String("host", host), zap.Error(err))
			configResult.Hosts[host] = &HostResult{
				Host:    host,
				Success: false,
				Error:   err,
			}
			configResult.Success = false
			continue
		}

		// Create directory and move to final location using local exec
		mkdirCmd := exec.Command("sudo", "mkdir", "-p", filepath.Dir(remotePath))
		if err := mkdirCmd.Run(); err != nil {
			c.logger.Error("Failed to create directory", zap.String("host", host), zap.Error(err))
			configResult.Hosts[host] = &HostResult{
				Host:    host,
				Success: false,
				Error:   err,
			}
			configResult.Success = false
			// Best-effort cleanup of the staging copy. The host has already been
			// recorded as failed with the error that matters; the temp path is a
			// fixed name that the next distribution overwrites, so a failed
			// unlink leaks nothing but one stale file.
			_ = os.Remove(localTempFile)
			continue
		}

		mvCmd := exec.Command("sudo", "mv", "-f", localTempFile, remotePath)
		if err := mvCmd.Run(); err != nil {
			c.logger.Error("Failed to move local config", zap.String("host", host), zap.Error(err))
			configResult.Hosts[host] = &HostResult{
				Host:    host,
				Success: false,
				Error:   err,
			}
			configResult.Success = false
			_ = os.Remove(localTempFile)
			continue
		}

		configResult.Hosts[host] = &HostResult{
			Host:    host,
			Success: true,
		}
		c.logger.Debug("Local config distributed", zap.String("host", host))
	}

	// Handle remote hosts - use dispatch.Copy
	if len(remoteHosts) > 0 {
		c.logger.Debug("Copying to remote hosts", zap.Strings("remote_hosts", remoteHosts))
		// For remote hosts, use cat + ssh + sudo tee to handle privileged paths
		for _, host := range remoteHosts {
			// First create directory
			mkdirCmd := fmt.Sprintf("sudo mkdir -p %s", filepath.Dir(remotePath))
			mkdirResult, err := c.Exec(ctx, []string{host}, mkdirCmd)
			if err != nil {
				c.logger.Error("Failed to create directory", zap.String("host", host), zap.Error(err))
				configResult.Hosts[host] = &HostResult{
					Host:    host,
					Success: false,
					Error:   err,
				}
				configResult.Success = false
				continue
			}
			if !mkdirResult.AllSuccess() {
				configResult.Hosts[host] = &HostResult{
					Host:    host,
					Success: false,
					Error:   fmt.Errorf("mkdir failed"),
				}
				configResult.Success = false
				continue
			}

			// Use cat | ssh | sudo tee to copy file with root permissions
			// Write content to temp file first
			if err := os.WriteFile(localTempFile, []byte(content), 0644); err != nil {
				c.logger.Error("Failed to write temp file", zap.Error(err))
				configResult.Hosts[host] = &HostResult{
					Host:    host,
					Success: false,
					Error:   err,
				}
				configResult.Success = false
				continue
			}

			// Copy using ssh with sudo tee (direct execution via dispatch)
			// First read file content
			fileContent, err := os.ReadFile(localTempFile)
			if err != nil {
				c.logger.Error("Failed to read temp file", zap.Error(err))
				configResult.Hosts[host] = &HostResult{
					Host:    host,
					Success: false,
					Error:   err,
				}
				configResult.Success = false
				continue
			}

			// Transfer via base64 so binary content survives intact. A single
			// `echo <base64>` breaks for large files: one shell argument is
			// capped at MAX_ARG_STRLEN (128 KiB on Linux), so a multi-MB binary
			// (e.g. the haify-proxy WAN binary) fails with "Argument list too long"
			// and the file is never written. Small content keeps the fast single
			// command; large content is streamed in sub-128 KiB base64 chunks.
			encoded := base64.StdEncoding.EncodeToString(fileContent)
			var copyErr error
			if len(encoded) <= maxInlineB64Len {
				cmd := fmt.Sprintf("echo %s | base64 -d | sudo tee %s > /dev/null",
					fmt.Sprintf("%q", encoded), remotePath)
				r, err := c.Exec(ctx, []string{host}, cmd)
				switch {
				case err != nil:
					copyErr = err
				case !r.AllSuccess():
					copyErr = fmt.Errorf("copy failed")
				}
			} else {
				copyErr = c.writeRemoteFileChunked(ctx, host, remotePath, encoded)
			}
			if copyErr != nil {
				c.logger.Error("Failed to copy config", zap.String("host", host), zap.Error(copyErr))
				configResult.Hosts[host] = &HostResult{
					Host:    host,
					Success: false,
					Error:   copyErr,
				}
				configResult.Success = false
				continue
			}

			configResult.Hosts[host] = &HostResult{
				Host:    host,
				Success: true,
			}
			c.logger.Debug("Remote config distributed", zap.String("host", host))
		}
		// The remote copies are done with the staging file; same best-effort
		// reasoning as the local branch above.
		_ = os.Remove(localTempFile)
	}

	// Run post-command if specified
	if options.postCommand != "" {
		_, _ = c.Exec(ctx, hosts, options.postCommand)
	}

	return configResult, nil
}

// DistributeSecret writes content to relPath — resolved against the SSH login
// user's home directory — at mode 0600, WITHOUT the content ever appearing in a
// command line.
//
// DistributeConfig cannot be used for secrets. It base64-encodes the payload
// into the remote command string, so the bytes are visible in `ps` on the node
// for the duration of the copy and in anything that logs commands along the
// way. That is harmless for a DRBD config and disqualifying for an object-store
// secret key. This path hands the content to the scp protocol over the existing
// SSH session instead, where it travels in the encrypted stream and the mode is
// set by the receiving end before the file has any content.
//
// relPath is relative on purpose: the copy runs as the login user, who cannot
// necessarily write /etc or /run, and a 0600 file in that user's own home needs
// no privilege at all. Callers are expected to have created the parent
// directory 0700 first, so the file is private even for the instant between
// create and chmod.
func (c *Client) DistributeSecret(ctx context.Context, hosts []string, content, relPath string) (*ConfigResult, error) {
	if filepath.IsAbs(relPath) {
		return nil, fmt.Errorf("DistributeSecret: %q must be relative to the login user's home", relPath)
	}

	result := &ConfigResult{Path: relPath, Success: true, Hosts: make(map[string]*HostResult)}

	// Stage the payload in a private directory on the controller. A 0600 file
	// inside a 0700 directory is never readable by another local user, not even
	// between os.MkdirTemp and os.WriteFile.
	dir, err := os.MkdirTemp("", "haify-secret-")
	if err != nil {
		return nil, fmt.Errorf("DistributeSecret: stage secret: %w", err)
	}
	// This is the one cleanup in this file that is worth a word if it fails: the
	// staged file is the secret itself, and leaving it on the controller's disk
	// defeats the point of taking the scp path in the first place. Nothing can
	// be done about it from here — the caller's operation may well have
	// succeeded — so it is logged rather than returned, loudly enough that an
	// operator can go and shred it.
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			c.logger.Warn("failed to remove staged secret from the controller; remove it by hand",
				zap.String("dir", dir), zap.Error(err))
		}
	}()
	local := filepath.Join(dir, filepath.Base(relPath))
	if err := os.WriteFile(local, []byte(content), 0600); err != nil {
		return nil, fmt.Errorf("DistributeSecret: stage secret: %w", err)
	}

	var localHosts, remoteHosts []string
	localAddrs := getLocalIPs()
	for _, host := range hosts {
		if isLocalIP(host, localAddrs) {
			localHosts = append(localHosts, host)
		} else {
			remoteHosts = append(remoteHosts, host)
		}
	}

	// A node that is this machine skips SSH entirely; $HOME is the controller
	// process's own, which is the same account the local Exec branch runs as.
	for _, host := range localHosts {
		hr := &HostResult{Host: host, Success: true}
		home, err := os.UserHomeDir()
		if err == nil {
			dest := filepath.Join(home, relPath)
			if err = os.MkdirAll(filepath.Dir(dest), 0700); err == nil {
				err = os.WriteFile(dest, []byte(content), 0600)
			}
		}
		if err != nil {
			hr.Success, hr.Error = false, err
			result.Success = false
		}
		result.Hosts[host] = hr
	}

	if len(remoteHosts) > 0 {
		copyResult, err := copyWithin(ctx, secretCopyTimeout, func() (*dispatch.CopyResult, error) {
			return c.dispatch.Copy(ctx, remoteHosts, local, relPath, dispatch.WithCopyMode(0600))
		})
		if err != nil {
			return nil, fmt.Errorf("DistributeSecret: copy to %v: %w", remoteHosts, err)
		}
		for _, host := range remoteHosts {
			hr := &HostResult{Host: host, Success: false, Error: fmt.Errorf("no result for host")}
			if r := copyResult.Hosts[host]; r != nil {
				hr.Success, hr.Error = r.Success, r.Error
			}
			if !hr.Success {
				result.Success = false
			}
			result.Hosts[host] = hr
		}
	}

	c.logger.Debug("Secret distributed",
		zap.Strings("hosts", hosts), zap.String("path", relPath), zap.Bool("success", result.Success))
	return result, nil
}

// secretCopyTimeout bounds copying a secret file: a few hundred bytes, so
// anything longer is a transfer that is not coming back.
const secretCopyTimeout = 2 * time.Minute

// copyWithin runs a dispatch copy but gives up after d or when ctx ends.
//
// dispatch's Copy stops watching its context once the per-host transfers have
// started and then waits for all of them, so one SFTP session on a stalled
// connection blocks its caller forever. Seen on a scheduled backup to a node
// whose network had paused: the run never returned, and the schedule's
// one-run-at-a-time guard then skipped every later tick. The abandoned copy
// finishes or dies with its connection; its result is discarded.
func copyWithin(ctx context.Context, d time.Duration, copyFn func() (*dispatch.CopyResult, error)) (*dispatch.CopyResult, error) {
	type outcome struct {
		res *dispatch.CopyResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := copyFn()
		done <- outcome{res, err}
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case o := <-done:
		return o.res, o.err
	case <-timer.C:
		return nil, fmt.Errorf("copy did not finish within %s", d)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// InstallFile installs a local file — typically a binary — at an absolute
// path on every host, owned by root at the given mode. DistributeConfig cannot
// carry it: that path puts the content on a command line, which an executable
// of several megabytes does not fit on. The file travels by SFTP into the login
// user's home instead and is moved into place with sudo install.
func (c *Client) InstallFile(ctx context.Context, hosts []string, localPath, remotePath string, mode os.FileMode) (*ConfigResult, error) {
	if !filepath.IsAbs(remotePath) {
		return nil, fmt.Errorf("InstallFile: %q must be absolute", remotePath)
	}
	result := &ConfigResult{Path: remotePath, Success: true, Hosts: make(map[string]*HostResult)}
	modeArg := strconv.FormatUint(uint64(mode.Perm()), 8)

	var localHosts, remoteHosts []string
	localAddrs := getLocalIPs()
	for _, host := range hosts {
		if isLocalIP(host, localAddrs) {
			localHosts = append(localHosts, host)
		} else {
			remoteHosts = append(remoteHosts, host)
		}
	}

	for _, host := range localHosts {
		hr := &HostResult{Host: host, Success: true}
		if out, err := localCommand(ctx, fmt.Sprintf("sudo install -D -m %s %s %s", modeArg, shellQuote(localPath), shellQuote(remotePath))).CombinedOutput(); err != nil {
			hr.Success, hr.Error = false, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
			result.Success = false
		}
		result.Hosts[host] = hr
	}

	if len(remoteHosts) > 0 {
		staged := ".haify-install-" + filepath.Base(remotePath)
		copyResult, err := c.dispatch.Copy(ctx, remoteHosts, localPath, staged, dispatch.WithCopyMode(0700))
		if err != nil {
			return nil, fmt.Errorf("InstallFile: copy to %v: %w", remoteHosts, err)
		}
		var copied []string
		for _, host := range remoteHosts {
			r := copyResult.Hosts[host]
			if r == nil || !r.Success {
				hr := &HostResult{Host: host, Error: fmt.Errorf("copy failed")}
				if r != nil && r.Error != nil {
					hr.Error = r.Error
				}
				result.Hosts[host] = hr
				result.Success = false
				continue
			}
			copied = append(copied, host)
		}
		if len(copied) > 0 {
			cmd := fmt.Sprintf(`sudo install -D -m %s "$HOME/%s" %s; rc=$?; rm -f "$HOME/%s"; exit $rc`,
				modeArg, staged, remotePath, staged)
			execResult, err := c.Exec(ctx, copied, cmd)
			if err != nil {
				return nil, fmt.Errorf("InstallFile: install on %v: %w", copied, err)
			}
			for _, host := range copied {
				hr := &HostResult{Host: host, Error: fmt.Errorf("no result for host")}
				if r := execResult.Hosts[host]; r != nil {
					hr = r
				}
				if !hr.Success {
					result.Success = false
				}
				result.Hosts[host] = hr
			}
		}
	}
	return result, nil
}

// maxInlineB64Len bounds the base64 payload sent as a single `echo` argument.
// Linux caps one argument at MAX_ARG_STRLEN (128 KiB); stay well under it so the
// fast single-command path never trips "Argument list too long".
const maxInlineB64Len = 100 * 1024

// writeRemoteFileChunked writes base64-encoded content to remotePath on host by
// streaming it in sub-128 KiB chunks (each a single safe argument), then decoding
// once server-side. This is what lets DistributeConfig ship multi-MB binaries
// (e.g. haify-proxy) that overflow a single-argument echo. base64's alphabet
// (A-Za-z0-9+/=) contains no single-quote, so each chunk is quote-safe.
func (c *Client) writeRemoteFileChunked(ctx context.Context, host, remotePath, encoded string) error {
	tmp := remotePath + ".b64.part"
	// Start from an empty temp file.
	if r, err := c.Exec(ctx, []string{host}, fmt.Sprintf("sudo sh -c ': > %s'", tmp)); err != nil {
		return fmt.Errorf("init temp file: %w", err)
	} else if !r.AllSuccess() {
		return fmt.Errorf("init temp file on %s failed", host)
	}
	const chunk = maxInlineB64Len
	for i := 0; i < len(encoded); i += chunk {
		end := i + chunk
		if end > len(encoded) {
			end = len(encoded)
		}
		cmd := fmt.Sprintf("printf '%%s' '%s' | sudo tee -a %s > /dev/null", encoded[i:end], tmp)
		if r, err := c.Exec(ctx, []string{host}, cmd); err != nil {
			return fmt.Errorf("append chunk: %w", err)
		} else if !r.AllSuccess() {
			return fmt.Errorf("append chunk on %s failed", host)
		}
	}
	// Decode beside the target and rename it into place. Writing through the
	// target — what this used to do — fails with "Text file busy" when it is a
	// running executable: every re-push of the haify-proxy binary to a node
	// whose proxy was up. A rename replaces a busy file, and leaves the target
	// whole if anything before it fails. The new file takes the old one's mode
	// and owner, as writing into it did.
	next := remotePath + ".haify-new"
	cmd := fmt.Sprintf("sudo sh -c 'base64 -d %[1]s > %[2]s && { [ ! -e %[3]s ] || { chmod --reference=%[3]s %[2]s && chown --reference=%[3]s %[2]s; }; } && mv -f %[2]s %[3]s && rm -f %[1]s'",
		tmp, next, remotePath)
	if r, err := c.Exec(ctx, []string{host}, cmd); err != nil {
		return fmt.Errorf("decode remote file: %w", err)
	} else if !r.AllSuccess() {
		return fmt.Errorf("decode remote file on %s failed", host)
	}
	return nil
}

// DeleteConfig removes a config file from all nodes
func (c *Client) DeleteConfig(ctx context.Context, hosts []string, remotePath string) error {
	c.logger.Info("Deleting config", zap.String("path", remotePath))

	_, err := c.Exec(ctx, hosts, fmt.Sprintf("sudo rm -f %s", remotePath))
	return err
}

// ConfigOption configures config distribution
type ConfigOption func(*configOptions)

type configOptions struct {
	backup      bool
	postCommand string
}

// WithBackup enables backup of existing config
func WithBackup(backup bool) ConfigOption {
	return func(o *configOptions) {
		o.backup = backup
	}
}

// WithPostCommand sets a command to run after distribution
func WithPostCommand(cmd string) ConfigOption {
	return func(o *configOptions) {
		o.postCommand = cmd
	}
}
