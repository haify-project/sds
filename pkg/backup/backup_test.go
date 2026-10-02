package backup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDeploy records every command and secret the backend sends to a node. The
// recording is the point: several tests below assert on what did NOT appear in
// a command line.
type fakeDeploy struct {
	execs   []string
	secrets []string

	execFunc func(cmd string) (*Result, error)
	putErr   error
}

func (f *fakeDeploy) Exec(ctx context.Context, hosts []string, cmd string) (*Result, error) {
	f.execs = append(f.execs, cmd)
	if f.execFunc != nil {
		return f.execFunc(cmd)
	}
	return okResult(hosts, ""), nil
}

func (f *fakeDeploy) PutSecret(ctx context.Context, hosts []string, content, relPath string) (*Result, error) {
	f.secrets = append(f.secrets, content)
	if f.putErr != nil {
		return nil, f.putErr
	}
	return okResult(hosts, ""), nil
}

func okResult(hosts []string, out string) *Result {
	r := &Result{Hosts: map[string]*HostResult{}}
	for _, h := range hosts {
		r.Hosts[h] = &HostResult{Host: h, Output: out, Success: true}
	}
	return r
}

func failResult(hosts []string, out string) *Result {
	r := &Result{Hosts: map[string]*HostResult{}}
	for _, h := range hosts {
		r.Hosts[h] = &HostResult{Host: h, Output: out, Success: false}
	}
	return r
}

func s3Target() TargetSpec {
	return TargetSpec{
		Name: "offsite", Kind: KindS3, Bucket: "sds-backups", Prefix: "clusterA",
		Endpoint: "https://s3.example.com", User: "AKIAEXAMPLE", Secret: "s3cr3t-key",
	}
}

// The whole reason this package writes credentials through PutSecret rather
// than through a command is that a command line is public: `ps` on the node
// shows it, and every layer in between logs it. If a change ever routes the
// secret through Exec, this test is what catches it.
func TestPrepareNeverPutsTheSecretInACommandLine(t *testing.T) {
	dep := &fakeDeploy{}
	sess, err := NewRclone().Prepare(context.Background(), dep, "10.0.0.1", s3Target())
	require.NoError(t, err)

	require.Len(t, dep.secrets, 1, "the config must travel through PutSecret")
	assert.Contains(t, dep.secrets[0], "s3cr3t-key")

	for _, cmd := range dep.execs {
		assert.NotContains(t, cmd, "s3cr3t-key", "the secret leaked into a command line: %s", cmd)
	}
	// And nothing the session renders afterwards may carry it either.
	assert.NotContains(t, sess.PushCmd("x.img", 1<<30), "s3cr3t-key")
	assert.NotContains(t, sess.PullCmd("x.img"), "s3cr3t-key")
}

func TestPrepareCreatesAPrivateDirectoryBeforeWritingTheSecret(t *testing.T) {
	dep := &fakeDeploy{}
	_, err := NewRclone().Prepare(context.Background(), dep, "10.0.0.1", s3Target())
	require.NoError(t, err)

	require.NotEmpty(t, dep.execs)
	assert.Contains(t, dep.execs[0], "chmod 0700",
		"the parent must be private before the file lands, or the file is briefly readable")
}

func TestPreflightNamesTheMissingDependency(t *testing.T) {
	dep := &fakeDeploy{execFunc: func(cmd string) (*Result, error) {
		return failResult([]string{"10.0.0.1"}, "rclone is not installed"), nil
	}}
	err := NewRclone().Preflight(context.Background(), dep, "10.0.0.1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rclone is required on 10.0.0.1")
}

func TestPushCmdSizesS3ChunksForTheObject(t *testing.T) {
	dep := &fakeDeploy{}
	sess, err := NewRclone().Prepare(context.Background(), dep, "10.0.0.1", s3Target())
	require.NoError(t, err)

	// A small object keeps S3's 5 MiB minimum.
	assert.Contains(t, sess.PushCmd("v.img", 1<<30), "--s3-chunk-size 5M")

	// A 4 TiB object at 5 MiB per part would need ~880k parts, far past S3's
	// 10k limit; the chunk has to grow with the object or the upload dies
	// partway through having transferred terabytes.
	cmd := sess.PushCmd("v.img", 4<<40)
	assert.NotContains(t, cmd, "--s3-chunk-size 5M")
	assert.Contains(t, cmd, "--s3-chunk-size 467M")
}

func TestS3ChunkMiBAlwaysFitsThePartLimit(t *testing.T) {
	for _, size := range []uint64{1, 1 << 20, 1 << 30, 1 << 40, 16 << 40} {
		chunk := s3ChunkMiB(size) * 1024 * 1024
		parts := (size + chunk - 1) / chunk
		assert.LessOrEqual(t, parts, uint64(s3MaxParts), "size %d needs %d parts", size, parts)
	}
}

func TestRemotePathLayout(t *testing.T) {
	dep := &fakeDeploy{}
	s3, err := NewRclone().Prepare(context.Background(), dep, "h", s3Target())
	require.NoError(t, err)
	assert.Contains(t, s3.PullCmd("data/b1/volume-0.img"),
		"'sdsbackup:sds-backups/clusterA/data/b1/volume-0.img'")

	smb, err := NewRclone().Prepare(context.Background(), dep, "h", TargetSpec{
		Name: "nas", Kind: KindSMB, Host: "nas.lan", Share: "backups", User: "u", Secret: "p",
	})
	require.NoError(t, err)
	assert.Contains(t, smb.PullCmd("data/b1/volume-0.img"),
		"'sdsbackup:backups/data/b1/volume-0.img'")

	dav, err := NewRclone().Prepare(context.Background(), dep, "h", TargetSpec{
		Name: "dav", Kind: KindWebDAV, Endpoint: "https://nas.lan/dav", User: "u", Secret: "p",
	})
	require.NoError(t, err)
	assert.Contains(t, dav.PullCmd("data/b1/volume-0.img"), "'sdsbackup:data/b1/volume-0.img'")
}

func TestSizeBytesReadsRcloneJSON(t *testing.T) {
	dep := &fakeDeploy{execFunc: func(cmd string) (*Result, error) {
		if strings.Contains(cmd, "size --json") {
			return okResult([]string{"h"}, "NOTICE: something\n{\"count\":1,\"bytes\":4294967296}"), nil
		}
		return okResult([]string{"h"}, ""), nil
	}}
	sess, err := NewRclone().Prepare(context.Background(), dep, "h", s3Target())
	require.NoError(t, err)

	n, err := sess.SizeBytes(context.Background(), "v.img")
	require.NoError(t, err)
	assert.Equal(t, uint64(4294967296), n)
}

// "The object is missing" and "the object is empty" must never collapse into
// the same answer: this call is the verification step of a backup, and a zero
// returned for an unknown size would pass a size comparison against a
// zero-byte volume and record a backup that does not exist.
func TestSizeBytesRefusesToGuess(t *testing.T) {
	for name, out := range map[string]string{
		"missing object": `{"count":0,"bytes":0}`,
		"garbage":        "Failed to size: directory not found",
		"empty":          "",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseRcloneSize(out)
			require.Error(t, err)
		})
	}
}

func TestObscureRoundTrips(t *testing.T) {
	for _, plain := range []string{"", "hunter2", "a longer passphrase with spaces & symbols £"} {
		obscured, err := Obscure(plain)
		require.NoError(t, err)
		assert.NotEqual(t, plain, obscured)

		back, err := Reveal(obscured)
		require.NoError(t, err)
		assert.Equal(t, plain, back)
	}
}

// TestRevealMatchesRealRclone pins the obscure key against ciphertext produced
// by an actual rclone. The round-trip test above only proves Obscure and Reveal
// agree with each other, which they would even if the key were wrong — and a
// wrong key fails nowhere until an SMB or WebDAV target rejects the password at
// backup time. These values came from `rclone obscure` on rclone v1.60.1, and
// the reverse direction (real rclone revealing our output) was checked the same
// way; if rclone ever changes the encoding, this test is where it surfaces.
func TestRevealMatchesRealRclone(t *testing.T) {
	for obscured, plain := range map[string]string{
		"F834wybNHMHmZdJ4G6C4C1JsluAYHN94hIpWxRHiCYEY_w": "hunter2-test-PASS!",
		"j8m2raXkHZrQKOu83oAbTK8MYZEwXYNZ53_XGQ":         "sds-verify-1",
		"tzicA3LHaOznPFnnpNDEZtuZIrLryfc83aIoLQ":         "sds-verify-2",
	} {
		back, err := Reveal(obscured)
		require.NoError(t, err)
		assert.Equal(t, plain, back)
	}
}

func TestRevealRejectsSomethingThatIsNotObscured(t *testing.T) {
	_, err := Reveal("plain password!!")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an rclone-obscured secret")
}

func TestSMBConfigObscuresThePassword(t *testing.T) {
	conf, err := renderRcloneConfig(TargetSpec{
		Name: "nas", Kind: KindSMB, Host: "nas.lan", Share: "backups", User: "u", Secret: "hunter2",
	})
	require.NoError(t, err)
	assert.NotContains(t, conf, "hunter2", "rclone rejects a plain-text password outright")

	var pass string
	for _, line := range strings.Split(conf, "\n") {
		if strings.HasPrefix(line, "pass = ") {
			pass = strings.TrimPrefix(line, "pass = ")
		}
	}
	require.NotEmpty(t, pass)
	back, err := Reveal(pass)
	require.NoError(t, err)
	assert.Equal(t, "hunter2", back)
}

// A non-standard SMB port has to reach rclone as its own config key. Left
// inside `host`, rclone appends its default and dials "host:port:445".
func TestSMBHostCarriesANonStandardPortAsItsOwnKey(t *testing.T) {
	conf, err := renderRcloneConfig(TargetSpec{
		Name: "nas", Kind: KindSMB, Host: "192.0.2.10:4450", Share: "backups", User: "u", Secret: "p",
	})
	require.NoError(t, err)
	assert.Contains(t, conf, "host = 192.0.2.10\n")
	assert.Contains(t, conf, "port = 4450\n")
	assert.NotContains(t, conf, "host = 192.0.2.10:4450")
}

func TestSMBHostWithoutAPortIsLeftAlone(t *testing.T) {
	for _, host := range []string{"nas.lan", "[2001:db8::1]"} {
		conf, err := renderRcloneConfig(TargetSpec{
			Name: "nas", Kind: KindSMB, Host: host, Share: "backups", User: "u", Secret: "p",
		})
		require.NoError(t, err)
		assert.Contains(t, conf, "host = "+host+"\n")
		assert.NotContains(t, conf, "port = ")
	}
}

func TestSMBRejectsAMalformedPortAtAddTime(t *testing.T) {
	err := TargetSpec{Name: "nas", Kind: KindSMB, Host: "nas.lan:smb", Share: "b"}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid port")
}

func TestObscuredSecretIsPassedThroughButValidated(t *testing.T) {
	obscured, err := Obscure("hunter2")
	require.NoError(t, err)

	conf, err := renderRcloneConfig(TargetSpec{
		Name: "nas", Kind: KindSMB, Host: "nas.lan", Share: "b", User: "u",
		Secret: obscured, SecretIsObscured: true,
	})
	require.NoError(t, err)
	assert.Contains(t, conf, "pass = "+obscured)

	// A plain password pasted into the "already obscured" slot is a paste
	// error, and the only symptom later would be an auth failure.
	_, err = renderRcloneConfig(TargetSpec{
		Name: "nas", Kind: KindSMB, Host: "nas.lan", Share: "b", User: "u",
		Secret: "hunter2!", SecretIsObscured: true,
	})
	require.Error(t, err)
}

// rclone's config is line-oriented INI with unquoted values, so a newline in
// any field would inject or truncate settings — including, for instance,
// silently disabling TLS verification.
func TestConfigRejectsNewlineInjection(t *testing.T) {
	_, err := renderRcloneConfig(TargetSpec{
		Name: "x", Kind: KindS3, Bucket: "b", User: "k",
		Secret: "abc\nno_check_certificate = true",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain a newline")
}

func TestTargetValidation(t *testing.T) {
	require.Error(t, TargetSpec{Kind: KindS3}.Validate(), "a target needs a name")
	require.Error(t, TargetSpec{Name: "a b", Kind: KindS3, Bucket: "x", User: "u", Secret: "s"}.Validate())
	require.Error(t, TargetSpec{Name: "x", Kind: KindS3, Bucket: "b"}.Validate(), "s3 needs credentials")
	require.Error(t, TargetSpec{Name: "x", Kind: KindSMB, Host: "h"}.Validate(), "smb needs a share")
	require.Error(t, TargetSpec{Name: "x", Kind: KindWebDAV}.Validate(), "webdav needs a URL")
	require.NoError(t, s3Target().Validate())
}

func TestParseKind(t *testing.T) {
	for _, in := range []string{"s3", "S3", " smb ", "webdav"} {
		_, err := ParseKind(in)
		require.NoError(t, err, in)
	}
	_, err := ParseKind("ftp")
	require.Error(t, err)
}

func TestObjectPathCollapsesEmptyParts(t *testing.T) {
	assert.Equal(t, "data/b1/v.img", ObjectPath("", "data", "/b1/", "v.img"))
	assert.Equal(t, "", ObjectPath("", "/"))
}

func TestResultAllSuccessIsFalseWhenNothingRan(t *testing.T) {
	// An empty result must not read as success: a fake or a transport that
	// returned no hosts would otherwise let an upload that never happened pass
	// verification.
	assert.False(t, (&Result{Hosts: map[string]*HostResult{}}).AllSuccess())
	assert.False(t, (*Result)(nil).AllSuccess())
}

func TestRemoveIsIdempotent(t *testing.T) {
	dep := &fakeDeploy{}
	sess, err := NewRclone().Prepare(context.Background(), dep, "h", s3Target())
	require.NoError(t, err)
	require.NoError(t, sess.Remove(context.Background(), "v.img"))

	last := dep.execs[len(dep.execs)-1]
	assert.Contains(t, last, "lsf", "removal must check first so a missing object is not an error")
	assert.Contains(t, last, "deletefile")
}

func TestParseRcloneListSkipsNoticesAndDirectories(t *testing.T) {
	out := "2026/10/02 NOTICE: something\n" +
		`[{"Path":"data/data_1/manifest.json","Size":512,"IsDir":false},` +
		`{"Path":"data","Size":-1,"IsDir":true},` +
		`{"Path":"data/data_1/volume-0.img.gz","Size":1048576,"IsDir":false}]`
	objs, err := parseRcloneList(out)
	require.NoError(t, err)
	assert.Equal(t, []Object{
		{Path: "data/data_1/manifest.json", Bytes: 512},
		{Path: "data/data_1/volume-0.img.gz", Bytes: 1048576},
	}, objs)

	_, err = parseRcloneList("Failed to lsjson: directory not found")
	assert.Error(t, err)
}

func TestFailureDetailsDropRcloneNotices(t *testing.T) {
	r := &Result{Hosts: map[string]*HostResult{"n1": {Output: "<5>NOTICE: S3 bucket b: Streaming uploads using chunk size 5Mi\n<3>ERROR : connection refused", Success: false}}}
	assert.Equal(t, "n1: <3>ERROR : connection refused", r.FailureDetails())

	only := &Result{Hosts: map[string]*HostResult{"n1": {Output: "<5>NOTICE: just this", Success: false}}}
	assert.Contains(t, only.FailureDetails(), "just this")
}

// Removing an object that is gone must not run deletefile: on RustFS `lsf`
// of a missing object exits 0 with no output, and deletefile then fails. The
// rendered command runs in a real shell against an rclone that behaves so.
func TestRemoveOfAMissingObjectSucceedsWhereLsfExitsZero(t *testing.T) {
	bin := t.TempDir()
	stub := "#!/bin/sh\nfor a in \"$@\"; do case $a in lsf) exit 0;; deletefile) echo gone >&2; exit 1;; esac; done\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "rclone"), []byte(stub), 0o755))

	var ran string
	dep := &fakeDeploy{execFunc: func(cmd string) (*Result, error) {
		if !strings.Contains(cmd, "deletefile") {
			return okResult([]string{"n1"}, ""), nil
		}
		c := exec.Command("sh", "-c", cmd)
		c.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		out, err := c.CombinedOutput()
		ran = string(out)
		return &Result{Hosts: map[string]*HostResult{"n1": {Host: "n1", Output: ran, Success: err == nil}}}, nil
	}}
	sess, err := NewRclone().Prepare(context.Background(), dep, "n1",
		TargetSpec{Name: "t", Kind: KindS3, Bucket: "b", User: "k", Secret: "s"})
	require.NoError(t, err)
	assert.NoError(t, sess.Remove(context.Background(), "x/manifest.json"), ran)
}
