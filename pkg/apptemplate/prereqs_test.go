package apptemplate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pgProbeOK = `Warning: Permanently added 'node1' to the list of known hosts.
server=/usr/lib/postgresql/16/bin/postgres
client=/usr/lib/postgresql/16/bin/psql
admin=/usr/lib/postgresql/16/bin/pg_isready
init=/usr/lib/postgresql/16/bin/initdb
version=postgres (PostgreSQL) 16.4 (Debian 16.4-1.pgdg120+1)
uid=113
gid=120
missing=
`

func TestParseProbe(t *testing.T) {
	p := ParseProbe(pgProbeOK)
	assert.Equal(t, "/usr/lib/postgresql/16/bin/postgres", p.Server)
	assert.Equal(t, "/usr/lib/postgresql/16/bin/psql", p.Client)
	assert.Equal(t, "/usr/lib/postgresql/16/bin/pg_isready", p.Admin)
	assert.Equal(t, "/usr/lib/postgresql/16/bin/initdb", p.Init)
	assert.Equal(t, 113, p.UID)
	assert.Equal(t, 120, p.GID)
	assert.Empty(t, p.Missing)
	assert.Empty(t, p.PortBusy)

	p = ParseProbe("missing= ocf:heartbeat:IPaddr2 user:redis redis-server\nportbusy=LISTEN 0 511 0.0.0.0:6379 0.0.0.0:*\n")
	assert.Equal(t, []string{"ocf:heartbeat:IPaddr2", "user:redis", "redis-server"}, p.Missing)
	assert.Equal(t, -1, p.UID, "no uid line means the user is missing")
	assert.Equal(t, "LISTEN 0 511 0.0.0.0:6379 0.0.0.0:*", p.PortBusy)
}

func probeWith(edit func(*NodeProbe)) NodeProbe {
	p := ParseProbe(pgProbeOK)
	if edit != nil {
		edit(&p)
	}
	return p
}

func TestCheckNodes(t *testing.T) {
	s := Spec{Name: "orders", Engine: Postgres, ServiceIP: "10.0.0.50/24"}
	require.NoError(t, s.Normalize())

	tests := []struct {
		name  string
		nodes []NodeResult
		want  []string // substrings of the error; none means success
	}{
		{name: "consistent", nodes: []NodeResult{
			{"node1", probeWith(nil)}, {"node2", probeWith(nil)}}},
		{name: "minor versions may differ", nodes: []NodeResult{
			{"node1", probeWith(nil)},
			{"node2", probeWith(func(p *NodeProbe) { p.Version = "postgres (PostgreSQL) 16.2" })}}},
		{name: "uid differs", nodes: []NodeResult{
			{"node1", probeWith(nil)},
			{"node2", probeWith(func(p *NodeProbe) { p.UID, p.GID = 114, 121 })},
			{"node3", probeWith(nil)}},
			want: []string{"user postgres has different ids", "node1, node3: uid=113 gid=120", "node2: uid=114 gid=121",
				"belong to another user", "usermod"}},
		{name: "gid alone differs", nodes: []NodeResult{
			{"node1", probeWith(nil)},
			{"node2", probeWith(func(p *NodeProbe) { p.GID = 999 })}},
			want: []string{"node2: uid=113 gid=999"}},
		{name: "major version differs", nodes: []NodeResult{
			{"node1", probeWith(nil)},
			{"node2", probeWith(func(p *NodeProbe) {
				p.Version = "postgres (PostgreSQL) 15.8"
				p.Server = "/usr/lib/postgresql/15/bin/postgres"
				p.Client = "/usr/lib/postgresql/15/bin/psql"
				p.Admin = "/usr/lib/postgresql/15/bin/pg_isready"
				p.Init = "/usr/lib/postgresql/15/bin/initdb"
			})}},
			want: []string{"versions differ", "node1: 16", "node2: 15", "installed in different places"}},
		{name: "missing pieces", nodes: []NodeResult{
			{"node1", probeWith(func(p *NodeProbe) { p.Missing = []string{"ocf:heartbeat:IPaddr2"} })},
			{"node2", probeWith(func(p *NodeProbe) { p.Missing = []string{"postgres", "user:postgres"}; p.UID = -1 })}},
			want: []string{"node1 lacks ocf:heartbeat:IPaddr2", "node2 lacks postgres user:postgres", "resource-agents-extra",
				"postgresql"}},
		{name: "port busy", nodes: []NodeResult{
			{"node1", probeWith(func(p *NodeProbe) { p.PortBusy = "LISTEN 0 244 127.0.0.1:5432" })},
			{"node2", probeWith(nil)}},
			want: []string{"port 5432 is already in use on node1", "--port"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agreed, err := CheckNodes(s, tc.nodes)
			if len(tc.want) == 0 {
				require.NoError(t, err)
				assert.Equal(t, 113, agreed.UID)
				assert.Equal(t, "/usr/lib/postgresql/16/bin/postgres", agreed.Binaries.Server)
				return
			}
			require.Error(t, err)
			for _, w := range tc.want {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
	// A missing user is not also reported as an id mismatch.
	_, err := CheckNodes(s, []NodeResult{{"node1", probeWith(nil)},
		{"node2", probeWith(func(p *NodeProbe) { p.Missing = []string{"user:postgres"}; p.UID, p.GID = -1, -1 })}})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "different ids")
}

func TestCompatKey(t *testing.T) {
	assert.Equal(t, "16", compatKey(Postgres, "", "postgres (PostgreSQL) 16.4 (Debian 16.4-1)"))
	assert.Equal(t, "mariadb 10.11", compatKey(MySQL, "mariadb", "mariadbd  Ver 10.11.6-MariaDB-0+deb12u1 for debian-linux-gnu"))
	assert.Equal(t, "mysql 8.0", compatKey(MySQL, "mysql", "/usr/sbin/mysqld  Ver 8.0.36 for Linux on x86_64"))
	assert.Equal(t, "7.0", compatKey(Redis, "", "Redis server v=7.0.15 sha=00000000:0 malloc=jemalloc-5.3.0 bits=64"))
	assert.Equal(t, "weird", compatKey(Redis, "", " weird "))
}

func TestProbeScript(t *testing.T) {
	pg := Spec{Name: "orders", Engine: Postgres, ServiceIP: "10.0.0.50/24", Vector: true}
	require.NoError(t, pg.Normalize())
	script := ProbeScript(pg)
	assert.Contains(t, script, "/usr/lib/ocf/resource.d/heartbeat/$a")
	assert.Contains(t, script, "for a in Filesystem IPaddr2")
	assert.Contains(t, script, "id -u postgres")
	assert.Contains(t, script, `ss -Hltn "sport = :5432"`)
	assert.Contains(t, script, "vector.control")
	assert.True(t, strings.HasSuffix(script, "exit 0\n"), "the probe reports, it does not fail")

	redis := Spec{Name: "cache", Engine: Redis, ServiceIP: "10.0.0.50/24"}
	require.NoError(t, redis.Normalize())
	assert.Contains(t, ProbeScript(redis), "id -u redis")
	assert.NotContains(t, ProbeScript(redis), "vector")

	rustfs := Spec{Name: "objects", Engine: RustFS, ServiceIP: "10.0.0.50/24"}
	require.NoError(t, rustfs.Normalize())
	script = ProbeScript(rustfs)
	assert.Contains(t, script, "id -u rustfs")
	assert.Contains(t, script, "pick rustfs")
	assert.Contains(t, script, "pick curl")
	assert.Contains(t, script, `ss -Hltn "sport = :9001"`, "the console port must be free too")
}
