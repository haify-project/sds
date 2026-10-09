package wanproxy

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostUnits is a DeploymentClient that answers the unit-listing command with a
// different body per host — which is the whole point of that command, since each
// node runs a different set of leg instances.
type hostUnits struct {
	units map[string][]string // host -> "<unit> loaded active running" rows
	// reachFrom records which host the WAN reachability probe ran on.
	reachFrom   string
	reachFails  bool
	execErr     error
	listedHosts []string
}

func (h *hostUnits) DistributeConfig(context.Context, []string, string, string) (*Result, error) {
	return nil, nil
}

func (h *hostUnits) Exec(_ context.Context, hosts []string, cmd string) (*Result, error) {
	if h.execErr != nil {
		return nil, h.execErr
	}
	r := &Result{Hosts: map[string]*HostResult{}}

	if strings.Contains(cmd, "/dev/tcp/") {
		h.reachFrom = hosts[0]
		for _, host := range hosts {
			r.Hosts[host] = &HostResult{Host: host, Success: !h.reachFails}
		}
		return r, nil
	}

	h.listedHosts = append([]string(nil), hosts...)
	for _, host := range hosts {
		rows, known := h.units[host]
		r.Hosts[host] = &HostResult{Host: host, Success: known, Output: strings.Join(rows, "\n")}
	}
	return r, nil
}

func unitRow(legID string, active bool) string {
	state := "inactive dead"
	if active {
		state = "active running"
	}
	return "haify-proxy@" + legID + ".service loaded " + state + " Haify WAN replication proxy"
}

// openclawSpec mirrors the shape that exposed the bug in production: three
// primary-site replicas and one DR node.
func openclawSpec() MultiSpec {
	return MultiSpec{
		Resource:         "openclaw",
		PrimaryNodeAddrs: []string{"192.168.123.62", "192.168.123.227", "192.168.123.212"},
		DRNodeAddr:       "47.109.108.170",
		DRPublicEndpoint: "47.109.108.170",
		BaseWANPort:      43512,
		BaseDRBDPort:     7300,
	}
}

// The regression: a resource with more than one primary-site replica has one
// systemd instance per leg, so a per-resource "haify-proxy@openclaw" check names
// a unit that exists on no node and reports a healthy WAN as entirely down.
func TestStatusMultiSeesEveryLeg(t *testing.T) {
	m := openclawSpec()
	legs := m.Legs()
	require.Len(t, legs, 3)

	dr := []string{}
	units := map[string][]string{}
	for i, leg := range legs {
		units[m.PrimaryNodeAddrs[i]] = []string{unitRow(leg.Resource, true)}
		dr = append(dr, unitRow(leg.Resource, true))
	}
	units[m.DRNodeAddr] = dr

	f := &hostUnits{units: units}
	st, err := StatusMulti(context.Background(), f, m)
	require.NoError(t, err)

	require.Len(t, st.Legs, 3)
	for _, l := range st.Legs {
		assert.True(t, l.PrimaryActive, "leg %s primary", l.LegID)
		assert.True(t, l.DRActive, "leg %s DR", l.LegID)
	}
	assert.True(t, st.Healthy())
	assert.Empty(t, st.Unhealthy())

	// Leg names are per (resource, node), not per resource.
	assert.Equal(t, "openclaw_192-168-123-62", st.Legs[0].LegID)

	// One round trip covers every host, rather than one Exec per leg per node.
	assert.ElementsMatch(t,
		[]string{"192.168.123.62", "192.168.123.227", "192.168.123.212", "47.109.108.170"},
		f.listedHosts)
}

func TestStatusMultiReportsTheBrokenLegOnly(t *testing.T) {
	m := openclawSpec()
	legs := m.Legs()

	// node .62's dialer is down; the other two are fine.
	units := map[string][]string{
		"192.168.123.62":  {unitRow(legs[0].Resource, false)},
		"192.168.123.227": {unitRow(legs[1].Resource, true)},
		"192.168.123.212": {unitRow(legs[2].Resource, true)},
		"47.109.108.170": {
			unitRow(legs[0].Resource, true),
			unitRow(legs[1].Resource, true),
			unitRow(legs[2].Resource, true),
		},
	}

	st, err := StatusMulti(context.Background(), &hostUnits{units: units}, m)
	require.NoError(t, err)
	assert.False(t, st.Healthy())

	bad := st.Unhealthy()
	require.Len(t, bad, 1, "only the one broken leg may be reported")
	assert.Equal(t, "192.168.123.62", bad[0].PrimaryHost)
	assert.False(t, bad[0].PrimaryActive)
	assert.True(t, bad[0].DRActive)
}

// A node the controller cannot reach at all reports no units, which means its
// leg is down — not that the whole query failed.
func TestStatusMultiTreatsUnreachableNodeAsDownLeg(t *testing.T) {
	m := openclawSpec()
	legs := m.Legs()
	units := map[string][]string{
		// .62 absent entirely: Exec reports failure for that host.
		"192.168.123.227": {unitRow(legs[1].Resource, true)},
		"192.168.123.212": {unitRow(legs[2].Resource, true)},
		"47.109.108.170":  {unitRow(legs[1].Resource, true), unitRow(legs[2].Resource, true)},
	}

	st, err := StatusMulti(context.Background(), &hostUnits{units: units}, m)
	require.NoError(t, err)
	require.Len(t, st.Unhealthy(), 1)
	assert.Equal(t, "192.168.123.62", st.Unhealthy()[0].PrimaryHost)
}

// Probing the DR from a node whose own dialer is dead reports "WAN unreachable"
// for what is really a dead node, pointing at the wrong end of the link.
func TestStatusMultiProbesFromALiveNode(t *testing.T) {
	m := openclawSpec()
	legs := m.Legs()
	units := map[string][]string{
		"192.168.123.62":  {unitRow(legs[0].Resource, false)},
		"192.168.123.227": {unitRow(legs[1].Resource, true)},
		"192.168.123.212": {unitRow(legs[2].Resource, true)},
		"47.109.108.170":  {unitRow(legs[1].Resource, true), unitRow(legs[2].Resource, true)},
	}

	f := &hostUnits{units: units}
	_, err := StatusMulti(context.Background(), f, m)
	require.NoError(t, err)
	assert.Equal(t, "192.168.123.227", f.reachFrom,
		"the probe must run from a node whose dialer is actually up")
}

func TestStatusMultiUnreachableWAN(t *testing.T) {
	m := openclawSpec()
	legs := m.Legs()
	units := map[string][]string{}
	dr := []string{}
	for i, leg := range legs {
		units[m.PrimaryNodeAddrs[i]] = []string{unitRow(leg.Resource, true)}
		dr = append(dr, unitRow(leg.Resource, true))
	}
	units[m.DRNodeAddr] = dr

	st, err := StatusMulti(context.Background(), &hostUnits{units: units, reachFails: true}, m)
	require.NoError(t, err)
	assert.Empty(t, st.Unhealthy(), "every leg is up")
	assert.False(t, st.WANReachable)
	assert.False(t, st.Healthy(), "an unreachable DR endpoint is still unhealthy")
}

// A single-replica WAN resource keeps the historic per-resource unit name, so
// existing deployments must not start reporting a missing unit.
func TestStatusMultiSingleReplicaKeepsLegacyName(t *testing.T) {
	m := MultiSpec{
		Resource:         "legacy",
		PrimaryNodeAddrs: []string{"10.0.0.1"},
		DRNodeAddr:       "10.0.0.2",
		DRPublicEndpoint: "203.0.113.2",
		BaseWANPort:      43512,
		BaseDRBDPort:     7001,
	}
	units := map[string][]string{
		"10.0.0.1": {unitRow("legacy", true)},
		"10.0.0.2": {unitRow("legacy", true)},
	}

	st, err := StatusMulti(context.Background(), &hostUnits{units: units}, m)
	require.NoError(t, err)
	require.Len(t, st.Legs, 1)
	assert.Equal(t, "legacy", st.Legs[0].LegID)
	assert.True(t, st.Healthy())
}

func TestStatusMultiValidates(t *testing.T) {
	_, err := StatusMulti(context.Background(), nil, openclawSpec())
	assert.Error(t, err, "a nil deployment client must be an error, not a panic")

	_, err = StatusMulti(context.Background(), &hostUnits{}, MultiSpec{Resource: "x"})
	assert.Error(t, err, "a spec with no primary-site nodes must be rejected")
}

func TestParseActiveUnits(t *testing.T) {
	got := parseActiveUnits(strings.Join([]string{
		"haify-proxy@a.service loaded active running Haify WAN replication proxy for a",
		"haify-proxy@b.service loaded inactive dead Haify WAN replication proxy for b",
		"  ", // blank rows and legend leftovers must be ignored
		"short row",
	}, "\n"))

	assert.True(t, got["haify-proxy@a.service"])
	assert.False(t, got["haify-proxy@b.service"])
	assert.Len(t, got, 2)
}

// MultiStatus with no legs is not "healthy by vacuous truth".
func TestEmptyMultiStatusIsNotHealthy(t *testing.T) {
	assert.False(t, (&MultiStatus{WANReachable: true}).Healthy())
	assert.False(t, (*MultiStatus)(nil).Healthy())
}

// A node's leg name encodes the address it had when the tunnel was provisioned.
// After a renumber (a DHCP lease that moves, a VM back on a different address)
// the computed name no longer matches the instance that is actually running and
// replicating — which must not be reported as an outage.
func TestStatusMultiToleratesRenumberedNode(t *testing.T) {
	m := openclawSpec()
	legs := m.Legs()

	// .62 still runs the leg it was given when it was .206.
	stale := "openclaw_192-168-123-206"
	units := map[string][]string{
		"192.168.123.62":  {unitRow(stale, true)},
		"192.168.123.227": {unitRow(legs[1].Resource, true)},
		"192.168.123.212": {unitRow(legs[2].Resource, true)},
		"47.109.108.170": {
			unitRow(stale, true),
			unitRow(legs[1].Resource, true),
			unitRow(legs[2].Resource, true),
		},
	}

	st, err := StatusMulti(context.Background(), &hostUnits{units: units}, m)
	require.NoError(t, err)
	assert.Empty(t, st.Unhealthy(), "a working tunnel under an old name is not an outage")
	assert.True(t, st.Healthy())

	// And it reports the leg id that actually exists, so the operator can act on it.
	assert.Equal(t, stale, st.Legs[0].LegID)
}

// A host running no instance at all is still down, reported under the name that
// was expected — that is what someone needs in order to go looking for it.
func TestStatusMultiMissingLegKeepsExpectedName(t *testing.T) {
	m := openclawSpec()
	legs := m.Legs()
	units := map[string][]string{
		"192.168.123.62":  {},
		"192.168.123.227": {unitRow(legs[1].Resource, true)},
		"192.168.123.212": {unitRow(legs[2].Resource, true)},
		"47.109.108.170":  {unitRow(legs[1].Resource, true), unitRow(legs[2].Resource, true)},
	}

	st, err := StatusMulti(context.Background(), &hostUnits{units: units}, m)
	require.NoError(t, err)
	require.Len(t, st.Unhealthy(), 1)
	assert.Equal(t, "openclaw_192-168-123-62", st.Unhealthy()[0].LegID)
}

// With more than one instance on a host, guessing would report some other
// resource's leg as this one's.
func TestStatusMultiAmbiguousHostDoesNotGuess(t *testing.T) {
	m := openclawSpec()
	legs := m.Legs()
	units := map[string][]string{
		"192.168.123.62":  {unitRow("openclaw_old-a", true), unitRow("openclaw_old-b", true)},
		"192.168.123.227": {unitRow(legs[1].Resource, true)},
		"192.168.123.212": {unitRow(legs[2].Resource, true)},
		"47.109.108.170":  {unitRow(legs[1].Resource, true), unitRow(legs[2].Resource, true)},
	}

	st, err := StatusMulti(context.Background(), &hostUnits{units: units}, m)
	require.NoError(t, err)
	require.Len(t, st.Unhealthy(), 1)
	assert.Equal(t, "openclaw_192-168-123-62", st.Unhealthy()[0].LegID)
}

// Leg names must come from the node's stable name, not its address, or a
// renumber orphans the tunnel.
func TestLegsUseNodeNamesWhenGiven(t *testing.T) {
	m := openclawSpec()
	m.PrimaryNodeKeys = []string{"node-a", "node-b", "node-e"}

	legs := m.Legs()
	require.Len(t, legs, 3)
	assert.Equal(t, "openclaw_node-a", legs[0].Resource)
	assert.Equal(t, "openclaw_node-e", legs[2].Resource)
	// The address is still what the leg is reached at.
	assert.Equal(t, "192.168.123.62", legs[0].PrimaryNodeAddr)

	// Renumbering the node must not change the leg's name.
	m.PrimaryNodeAddrs[0] = "192.168.123.99"
	assert.Equal(t, "openclaw_node-a", m.Legs()[0].Resource)
}

func TestLegsFallBackToAddressWithoutNames(t *testing.T) {
	m := openclawSpec()
	assert.Equal(t, "openclaw_192-168-123-62", m.Legs()[0].Resource)

	// A partially-filled key list falls back per entry rather than shifting.
	m.PrimaryNodeKeys = []string{"", "node-b"}
	legs := m.Legs()
	assert.Equal(t, "openclaw_192-168-123-62", legs[0].Resource)
	assert.Equal(t, "openclaw_node-b", legs[1].Resource)
	assert.Equal(t, "openclaw_192-168-123-212", legs[2].Resource)
}

func TestFindStaleLegsFindsRenumberedOrphan(t *testing.T) {
	m := openclawSpec()
	m.PrimaryNodeKeys = []string{"node-a", "node-b", "node-e"}
	legs := m.Legs()

	// node-a still runs the address-named leg from before the renumber, and the
	// DR still has its acceptor.
	units := map[string][]string{
		"192.168.123.62":  {unitRow("openclaw_192-168-123-206", true)},
		"192.168.123.227": {unitRow(legs[1].Resource, true)},
		"192.168.123.212": {unitRow(legs[2].Resource, true)},
		"47.109.108.170": {
			unitRow("openclaw_192-168-123-206", true),
			unitRow(legs[1].Resource, true),
			unitRow(legs[2].Resource, true),
		},
	}

	stale, err := FindStaleLegs(context.Background(), &hostUnits{units: units}, m)
	require.NoError(t, err)
	require.Len(t, stale, 2, "the orphan exists on the node and on the DR")
	assert.Equal(t, "192.168.123.62", stale[0].Host)
	assert.Equal(t, "openclaw_192-168-123-206", stale[0].LegID)
	assert.Equal(t, "47.109.108.170", stale[1].Host)
}

// A healthy resource has nothing stale, so repair is a no-op on it.
func TestFindStaleLegsIsEmptyWhenConsistent(t *testing.T) {
	m := openclawSpec()
	legs := m.Legs()
	units := map[string][]string{}
	dr := []string{}
	for i, leg := range legs {
		units[m.PrimaryNodeAddrs[i]] = []string{unitRow(leg.Resource, true)}
		dr = append(dr, unitRow(leg.Resource, true))
	}
	units[m.DRNodeAddr] = dr

	stale, err := FindStaleLegs(context.Background(), &hostUnits{units: units}, m)
	require.NoError(t, err)
	assert.Empty(t, stale)
}

// One resource's legs must never be swept up as another's.
func TestFindStaleLegsDoesNotTouchOtherResources(t *testing.T) {
	m := openclawSpec()
	legs := m.Legs()
	units := map[string][]string{
		"192.168.123.62":  {unitRow(legs[0].Resource, true), unitRow("openclawdata_x", true)},
		"192.168.123.227": {unitRow(legs[1].Resource, true)},
		"192.168.123.212": {unitRow(legs[2].Resource, true)},
		"47.109.108.170":  {unitRow(legs[0].Resource, true), unitRow(legs[1].Resource, true), unitRow(legs[2].Resource, true)},
	}

	stale, err := FindStaleLegs(context.Background(), &hostUnits{units: units}, m)
	require.NoError(t, err)
	assert.Empty(t, stale, "openclawdata is a different resource, not a stale openclaw leg")
}

// An unreachable node reports nothing; that is not evidence its leg is stale,
// and deleting on that basis would tear down a tunnel that is merely unseen.
func TestFindStaleLegsIgnoresUnreachableHosts(t *testing.T) {
	m := openclawSpec()
	legs := m.Legs()
	units := map[string][]string{
		"192.168.123.227": {unitRow(legs[1].Resource, true)},
		"192.168.123.212": {unitRow(legs[2].Resource, true)},
		"47.109.108.170":  {unitRow(legs[1].Resource, true), unitRow(legs[2].Resource, true)},
	}

	stale, err := FindStaleLegs(context.Background(), &hostUnits{units: units}, m)
	require.NoError(t, err)
	assert.Empty(t, stale)
}

func TestBelongsToResource(t *testing.T) {
	assert.True(t, belongsToResource("openclaw", "openclaw"))
	assert.True(t, belongsToResource("openclaw_node-a", "openclaw"))
	assert.False(t, belongsToResource("openclawdata", "openclaw"))
	assert.False(t, belongsToResource("other_node-a", "openclaw"))
}

// The one name shape that would make two resources' legs indistinguishable is
// refused at creation, which is what keeps FindStaleLegs safe.
func TestValidateResourceNameForWAN(t *testing.T) {
	existing := []string{"openclaw", "vmstore"}

	assert.NoError(t, ValidateResourceNameForWAN("archive", existing))
	assert.NoError(t, ValidateResourceNameForWAN("openclawdata", existing))
	assert.NoError(t, ValidateResourceNameForWAN("openclaw", existing), "renaming to itself is not a collision")

	err := ValidateResourceNameForWAN("openclaw_backup", existing)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openclaw")
}

func TestRemoveStaleLegsIsNoOpOnEmpty(t *testing.T) {
	assert.NoError(t, RemoveStaleLegs(context.Background(), &hostUnits{}, nil))
	assert.Error(t, RemoveStaleLegs(context.Background(), nil, []StaleLeg{{Host: "h", LegID: "l"}}))
}
