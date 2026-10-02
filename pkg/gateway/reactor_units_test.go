package gateway

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The unit drop-ins written for a running gateway must be the ones
// drbd-reactor itself generates, or the next start of a unit there would
// differ from what a reload produces. The cases are drbd-reactor's own
// (src/systemd.rs test_ocf_parse_to_env).

func TestParseStartActionMatchesReactor(t *testing.T) {
	u, err := parseStartAction("res1",
		"ocf:vendor1:agent1 name1\nk1=v1 \nk2=\"with whitespace\" k3=with\\ different\\ whitespace foo empty='' pass='*pass/'",
		"/usr/lib/ocf")
	require.NoError(t, err)
	assert.Equal(t, "ocf.rs@name1_res1.service", u.Name)
	assert.Equal(t, []string{
		`OCF_RESKEY_k1=v1`,
		`OCF_RESKEY_k2=with\x20whitespace`,
		`OCF_RESKEY_k3=with\x20different\x20whitespace`,
		`OCF_RESKEY_foo=`,
		`OCF_RESKEY_empty=`,
		`OCF_RESKEY_pass=\x2apass/`,
		`OCF_ROOT=/usr/lib/ocf`,
		`AGENT=/usr/lib/ocf/resource.d/vendor1/agent1`,
	}, u.Env)

	u, err = parseStartAction("res-1", "ocf:vendor1:agent1 name-1 do not care", "/usr/lib/ocf/")
	require.NoError(t, err)
	assert.Equal(t, `ocf.rs@name\x2d1_res\x2d1.service`, u.Name)
	assert.Equal(t, `AGENT=/usr/lib/ocf/resource.d/vendor1/agent1`, u.Env[len(u.Env)-1])
}

func TestPromoterUnitsFromISCSIConfig(t *testing.T) {
	cfg := testISCSIConfig(t, "iqn.2024-01.com.example:blk")
	cfg = strings.Replace(cfg, "allowed_initiators=",
		`allowed_initiators=\"iqn.2024-01.com.example:a iqn.2024-01.com.example:b\"`, 1)
	p, err := parsePromoterUnits(cfg)
	require.NoError(t, err)
	assert.Equal(t, "blk", p.Resource)
	assert.Equal(t, "Requires", p.TargetAs)

	var names []string
	for _, u := range p.Units {
		names = append(names, u.Name)
	}
	assert.Equal(t, []string{
		"ocf.rs@fs_cluster_private_blk.service",
		"ocf.rs@target_blk.service",
		"ocf.rs@lu1_blk.service",
		"ocf.rs@service_ip0_blk.service",
	}, names)
	assert.Equal(t, "iqn.2024-01.com.example:a iqn.2024-01.com.example:b", p.Units[1].Params["allowed_initiators"],
		"a quoted list reaches the agent as one value")

	lu := p.unitDropIn(2)
	assert.Contains(t, lu, "PartOf = drbd-services@blk.target\n")
	assert.Contains(t, lu, "BindsTo = drbd-promote@blk.service\n")
	assert.Contains(t, lu, "Requires = ocf.rs@target_blk.service\nAfter = ocf.rs@target_blk.service\n")
	assert.Contains(t, lu, "[Service]\nEnvironment= OCF_RESKEY_target_iqn=iqn.2024\\x2d01.com.example:blk\n")
	assert.NotContains(t, p.unitDropIn(0), "Requires =", "the first unit depends on nothing but the promotion")

	assert.Equal(t, dropInHeader+"[Unit]\n"+
		"Requires = ocf.rs@fs_cluster_private_blk.service\n"+
		"Requires = ocf.rs@target_blk.service\n"+
		"Requires = ocf.rs@lu1_blk.service\n"+
		"Requires = ocf.rs@service_ip0_blk.service\n", p.targetDropIn())
}

func TestDiffUnits(t *testing.T) {
	old := testISCSIConfig(t, "iqn.2024-01.com.example:blk")
	oldP, err := parsePromoterUnits(old)
	require.NoError(t, err)

	withACL := strings.Replace(old, "allowed_initiators=", "allowed_initiators=iqn.2024-01.com.example:a", 1)
	p, err := parsePromoterUnits(withACL)
	require.NoError(t, err)
	d := diffUnits(oldP, p)
	assert.Empty(t, d.Added)
	assert.Empty(t, d.Removed)
	require.Len(t, d.Changed, 1)
	assert.Equal(t, "ocf.rs@target_blk.service", d.Changed[0][1].Name)

	d = diffUnits(p, oldP)
	require.Len(t, d.Changed, 1)

	lines := strings.Split(old, "\n")
	var noLUN []string
	for _, l := range lines {
		if !strings.Contains(l, "iSCSILogicalUnit") {
			noLUN = append(noLUN, l)
		}
	}
	p, err = parsePromoterUnits(strings.Join(noLUN, "\n"))
	require.NoError(t, err)
	d = diffUnits(oldP, p)
	require.Len(t, d.Removed, 1)
	assert.Equal(t, "ocf.rs@lu1_blk.service", d.Removed[0].Name)
	assert.Empty(t, d.Changed, "the service IP's predecessor changes, not its parameters")
}

func TestLiveEditRefusesWhatCannotBeAppliedLive(t *testing.T) {
	old := testISCSIConfig(t, "iqn.2024-01.com.example:blk")
	_, err := planLiveEdit(old, strings.Replace(old, "portals=192.168.1.200:3260", "portals=192.168.1.201:3260", 1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "portals")

	_, err = planLiveEdit(old, strings.Replace(old, "run_fsck=no", "run_fsck=yes", 1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop and start the gateway")
}

func TestISCSILiveScriptModes(t *testing.T) {
	base := map[string]string{"iqn": "iqn.2024-01.com.example:blk", "portals": "1.2.3.4:3260", "implementation": "lio-t"}
	withList := func(list, user string) map[string]string {
		p := map[string]string{}
		for k, v := range base {
			p[k] = v
		}
		p["allowed_initiators"] = list
		p["incoming_username"] = user
		p["incoming_password"] = "pw"
		if user == "" {
			p["incoming_password"] = ""
		}
		return p
	}
	s, err := iscsiTargetLiveScript(withList("", ""), withList("IQN.2024-01.Com.Example:A", ""))
	require.NoError(t, err)
	assert.Contains(t, s, "want='iqn.2024-01.com.example:a'", "LIO keeps node names lower-cased")

	s, err = iscsiTargetLiveScript(withList("iqn.2024-01.com.example:a", ""), withList("", ""))
	require.NoError(t, err)
	assert.Contains(t, s, "want=''")
	assert.Contains(t, s, "generate_node_acls=1")

	_, err = iscsiTargetLiveScript(withList("", "u"), withList("", ""))
	require.Error(t, err, "dropping CHAP is not applied live")
}
