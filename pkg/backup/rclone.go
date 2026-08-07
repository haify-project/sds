package backup

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// remoteName is the rclone remote defined in every config this package renders.
// It is fixed because the config file is written per operation and holds
// exactly one remote.
const remoteName = "sdsbackup"

// nodeSecretDir is where a rendered rclone config lives on the node, relative
// to the SSH login user's home.
//
// Home rather than /etc or /run: the config is written over the SSH stream by
// the login user, who is not necessarily root, and a 0600 file in that user's
// home is both writable without a privilege escalation and unreadable by
// anyone else. The directory is created 0700 first, so the file is never
// exposed even for the instant between create and chmod.
const nodeSecretDir = ".sds-backup"

// s3MaxParts is the number of multipart chunks S3 allows for one object.
// `rclone rcat` streams an object of unknown length, so it cannot grow the
// chunk size on its own the way a file upload does; left at the 5 MiB default
// that caps a single object at ~48 GiB, which a storage volume passes easily.
// Every push therefore sizes the chunk from the exact byte count it is about
// to write. Getting this wrong does not corrupt anything — it fails the upload
// partway through, which is worse: it wastes the whole transfer.
const s3MaxParts = 9000

// s3MinChunkMiB is S3's minimum multipart chunk size.
const s3MinChunkMiB = 5

// RcloneBackend runs rclone on the storage node. See the package doc for why
// this, and not a pure-Go S3 client, is the default.
type RcloneBackend struct{}

// NewRclone returns the rclone-backed Backend.
func NewRclone() *RcloneBackend { return &RcloneBackend{} }

// Name implements Backend.
func (b *RcloneBackend) Name() string { return "rclone" }

// Preflight implements Backend. It runs before anything is snapshotted, so a
// node without rclone costs an SSH round trip rather than a snapshot, an
// upload attempt and a confusing partial failure.
func (b *RcloneBackend) Preflight(ctx context.Context, dep DeploymentClient, host string) error {
	if dep == nil {
		return fmt.Errorf("backup: deployment client is nil")
	}
	res, err := dep.Exec(ctx, []string{host},
		`command -v rclone >/dev/null 2>&1 || { echo "rclone is not installed"; exit 1; }; rclone version | head -n 1`)
	if err != nil {
		return fmt.Errorf("backup: rclone preflight on %s: %w", host, err)
	}
	if !res.AllSuccess() {
		return fmt.Errorf(
			"backup: rclone is required on %s and was not found (install it, e.g. `curl https://rclone.org/install.sh | sudo bash`): %s",
			host, res.FailureDetails())
	}
	return nil
}

// Prepare implements Backend: it renders the target's rclone config and writes
// it to the node at mode 0600 without the secret ever entering a command line.
func (b *RcloneBackend) Prepare(ctx context.Context, dep DeploymentClient, host string, target TargetSpec) (Session, error) {
	if dep == nil {
		return nil, fmt.Errorf("backup: deployment client is nil")
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	conf, err := renderRcloneConfig(target)
	if err != nil {
		return nil, err
	}

	id, err := randomID()
	if err != nil {
		return nil, err
	}
	rel := nodeSecretDir + "/" + id + ".conf"

	// The directory must be 0700 before the file lands in it: PutSecret sets the
	// file's own mode, but a private parent is what makes the window between the
	// two irrelevant.
	if _, err := dep.Exec(ctx, []string{host},
		fmt.Sprintf(`mkdir -p "$HOME/%s" && chmod 0700 "$HOME/%s"`, nodeSecretDir, nodeSecretDir)); err != nil {
		return nil, fmt.Errorf("backup: prepare credential directory on %s: %w", host, err)
	}
	res, err := dep.PutSecret(ctx, []string{host}, conf, rel)
	if err != nil {
		return nil, fmt.Errorf("backup: stage credentials on %s: %w", host, err)
	}
	if !res.AllSuccess() {
		return nil, fmt.Errorf("backup: stage credentials on %s failed: %s", host, res.FailureDetails())
	}

	return &rcloneSession{
		dep:     dep,
		host:    host,
		target:  target,
		cfgPath: `"$HOME/` + nodeSecretDir + `/` + id + `.conf"`,
		relPath: rel,
	}, nil
}

// rcloneSession is one node's prepared handle on a target.
type rcloneSession struct {
	dep    DeploymentClient
	host   string
	target TargetSpec

	// cfgPath is the shell expression naming the config, already quoted. It
	// contains no secret — only a path — so it is safe in a command line.
	cfgPath string
	relPath string
}

// remotePath maps an object key to the rclone remote path. The bucket (S3) or
// share (SMB) is part of the path rather than the config so one target can be
// re-pointed without re-staging credentials.
func (s *rcloneSession) remotePath(objectPath string) string {
	key := ObjectPath(s.target.Prefix, objectPath)
	switch s.target.Kind {
	case KindS3:
		return remoteName + ":" + ObjectPath(s.target.Bucket, key)
	case KindSMB:
		return remoteName + ":" + ObjectPath(s.target.Share, key)
	default: // WebDAV: the collection URL is the root.
		return remoteName + ":" + key
	}
}

// rcloneEnv prefixes a command with the config location. RCLONE_CONFIG carries
// a path, never a credential.
func (s *rcloneSession) rcloneEnv() string {
	return "RCLONE_CONFIG=" + s.cfgPath + " rclone --retries 3 --low-level-retries 10 --stats 0"
}

// PushCmd implements Session.
func (s *rcloneSession) PushCmd(objectPath string, sizeBytes uint64) string {
	cmd := s.rcloneEnv()
	if s.target.Kind == KindS3 {
		cmd += fmt.Sprintf(" --s3-chunk-size %dM", s3ChunkMiB(sizeBytes))
	}
	return cmd + " rcat " + shellQuote(s.remotePath(objectPath))
}

// PullCmd implements Session.
func (s *rcloneSession) PullCmd(objectPath string) string {
	return s.rcloneEnv() + " cat " + shellQuote(s.remotePath(objectPath))
}

// PutText implements Session. Content travels in the command line, so this is
// only for the manifest, which is metadata and holds no credentials. It is
// base64-encoded so arbitrary JSON survives the shell intact.
func (s *rcloneSession) PutText(ctx context.Context, objectPath, content string) error {
	enc := base64.StdEncoding.EncodeToString([]byte(content))
	cmd := fmt.Sprintf("set -o pipefail; printf %%s %s | base64 -d | %s",
		shellQuote(enc), s.PushCmd(objectPath, uint64(len(content))))
	res, err := s.dep.Exec(ctx, []string{s.host}, bash(cmd))
	if err != nil {
		return fmt.Errorf("backup: write %s: %w", objectPath, err)
	}
	if !res.AllSuccess() {
		return fmt.Errorf("backup: write %s failed: %s", objectPath, res.FailureDetails())
	}
	return nil
}

// SizeBytes implements Session by asking the remote how big the object it
// stored actually is. This is the end-to-end check that turns "the upload
// command exited 0" into "the far end holds every byte we sent".
func (s *rcloneSession) SizeBytes(ctx context.Context, objectPath string) (uint64, error) {
	cmd := s.rcloneEnv() + " size --json " + shellQuote(s.remotePath(objectPath))
	res, err := s.dep.Exec(ctx, []string{s.host}, cmd)
	if err != nil {
		return 0, fmt.Errorf("backup: stat %s: %w", objectPath, err)
	}
	if !res.AllSuccess() {
		return 0, fmt.Errorf("backup: stat %s failed: %s", objectPath, res.FailureDetails())
	}
	return parseRcloneSize(res.Output(s.host))
}

// Remove implements Session. Deleting something already gone is a no-op, so
// cleanup after a failed backup can be retried freely.
func (s *rcloneSession) Remove(ctx context.Context, objectPath string) error {
	remote := shellQuote(s.remotePath(objectPath))
	cmd := fmt.Sprintf("if %s lsf %s >/dev/null 2>&1; then %s deletefile %s; fi",
		s.rcloneEnv(), remote, s.rcloneEnv(), remote)
	res, err := s.dep.Exec(ctx, []string{s.host}, bash(cmd))
	if err != nil {
		return fmt.Errorf("backup: remove %s: %w", objectPath, err)
	}
	if !res.AllSuccess() {
		return fmt.Errorf("backup: remove %s failed: %s", objectPath, res.FailureDetails())
	}
	return nil
}

// Close implements Session by removing the staged credentials. A leftover file
// would be 0600 in a 0700 directory and therefore not a disclosure, but it
// would be a credential outliving its use, which is its own problem.
func (s *rcloneSession) Close(ctx context.Context) error {
	res, err := s.dep.Exec(ctx, []string{s.host}, fmt.Sprintf(`rm -f "$HOME/%s"`, s.relPath))
	if err != nil {
		return fmt.Errorf("backup: remove staged credentials on %s: %w", s.host, err)
	}
	if !res.AllSuccess() {
		return fmt.Errorf("backup: remove staged credentials on %s failed: %s", s.host, res.FailureDetails())
	}
	return nil
}

// renderRcloneConfig produces the single-remote rclone config for a target.
func renderRcloneConfig(t TargetSpec) (string, error) {
	if err := checkConfigValues(t); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\n", remoteName)
	switch t.Kind {
	case KindS3:
		region := t.Region
		if region == "" {
			// Many S3 clones reject an empty region outright, and AWS treats
			// us-east-1 as the neutral default, so this is the safe filler
			// rather than a guess about the operator's deployment.
			region = "us-east-1"
		}
		fmt.Fprintf(&b, "type = s3\nprovider = Other\nenv_auth = false\n")
		fmt.Fprintf(&b, "access_key_id = %s\nsecret_access_key = %s\nregion = %s\n", t.User, t.Secret, region)
		if t.Endpoint != "" {
			fmt.Fprintf(&b, "endpoint = %s\n", t.Endpoint)
		}
		// A backup user usually has PutObject on one bucket and nothing else;
		// rclone's default bucket existence check would fail on the missing
		// permission before a single byte moved.
		fmt.Fprintf(&b, "no_check_bucket = true\n")
	case KindSMB:
		pass, err := configPassword(t)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "type = smb\nhost = %s\n", t.Host)
		if t.User != "" {
			fmt.Fprintf(&b, "user = %s\n", t.User)
		}
		if pass != "" {
			fmt.Fprintf(&b, "pass = %s\n", pass)
		}
	case KindWebDAV:
		pass, err := configPassword(t)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "type = webdav\nurl = %s\nvendor = other\n", t.Endpoint)
		if t.User != "" {
			fmt.Fprintf(&b, "user = %s\n", t.User)
		}
		if pass != "" {
			fmt.Fprintf(&b, "pass = %s\n", pass)
		}
	default:
		return "", fmt.Errorf("backup: unknown target kind %q", t.Kind)
	}
	return b.String(), nil
}

// configPassword returns the obscured password for an SMB/WebDAV target.
func configPassword(t TargetSpec) (string, error) {
	if t.Secret == "" {
		return "", nil
	}
	if t.SecretIsObscured {
		// Trust it, but confirm it decodes: an operator who pasted a plain
		// password into the "already obscured" slot would otherwise get an
		// authentication failure at backup time with nothing pointing at the
		// cause.
		if _, err := Reveal(t.Secret); err != nil {
			return "", err
		}
		return t.Secret, nil
	}
	return Obscure(t.Secret)
}

// checkConfigValues rejects values that would corrupt the rendered config.
// rclone's config is line-oriented INI with unquoted values, so a newline in
// any field would silently inject or truncate settings.
func checkConfigValues(t TargetSpec) error {
	for name, v := range map[string]string{
		"endpoint": t.Endpoint, "region": t.Region, "bucket": t.Bucket,
		"host": t.Host, "share": t.Share, "user": t.User, "secret": t.Secret,
		"prefix": t.Prefix,
	} {
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("backup: target field %q must not contain a newline", name)
		}
	}
	return nil
}

// s3ChunkMiB sizes a multipart chunk so an object of sizeBytes fits inside
// S3's part limit. See s3MaxParts.
func s3ChunkMiB(sizeBytes uint64) uint64 {
	const miB = 1024 * 1024
	need := (sizeBytes/s3MaxParts + miB - 1) / miB
	if need < s3MinChunkMiB {
		return s3MinChunkMiB
	}
	return need
}

// parseRcloneSize reads `rclone size --json` output, which is
// {"count":1,"bytes":N}. Anything else is reported as an error rather than as
// a zero-byte object, because "the size is unknown" and "the object is empty"
// must never be confused on a verification path.
func parseRcloneSize(out string) (uint64, error) {
	out = strings.TrimSpace(out)
	// rclone may print notices before the JSON; take the last line that parses.
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var v struct {
			Count int64  `json:"count"`
			Bytes uint64 `json:"bytes"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(lines[i])), &v); err != nil {
			continue
		}
		if v.Count == 0 {
			return 0, fmt.Errorf("backup: object not found at the target")
		}
		return v.Bytes, nil
	}
	return 0, fmt.Errorf("backup: could not read object size from rclone output: %q", out)
}

// bash wraps a command that relies on pipefail or `if`, which are bash
// features; the deployment layer's default shell is not guaranteed to be bash.
func bash(cmd string) string {
	return "bash -c " + shellQuote(cmd)
}

// shellQuote renders s as a single-quoted shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// randomID returns a short random identifier for a per-operation file name.
func randomID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("backup: generate id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
