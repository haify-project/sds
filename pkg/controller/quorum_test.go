package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The two-site shape this exists for: three members, so the majority is two and
// one further loss is survivable.
const twoSiteVerbose = `openclaw node-id:0 role:Primary suspended:no
  volume:0 minor:2 disk:UpToDate
      quorum:yes open:yes blocked:no
  haify-b node-id:1 connection:Connected role:Secondary
    volume:0 replication:Established peer-disk:UpToDate
  iZ2vca1 node-id:3 connection:Connected role:Secondary
    volume:0 replication:Established peer-disk:UpToDate
`

func TestParseQuorumThreeMembersAllOnline(t *testing.T) {
	q := parseQuorum(twoSiteVerbose)
	assert.Equal(t, 3, q.Members)
	assert.Equal(t, 2, q.Required, "majority of three")
	assert.Equal(t, 3, q.Online)
	assert.True(t, q.HasQuorum)
	assert.Equal(t, 1, q.Tolerated, "one more node may be lost")
}

// A disconnected peer still counts toward the total — that is the point. Losing
// the WAN does not lower the bar, it just spends the margin.
func TestParseQuorumCountsDisconnectedPeersAsMembers(t *testing.T) {
	out := `openclaw node-id:0 role:Primary
      quorum:yes open:yes
  haify-b node-id:1 connection:Connected role:Secondary
  iZ2vca1 node-id:3 connection:Connecting role:Unknown
`
	q := parseQuorum(out)
	assert.Equal(t, 3, q.Members, "the unreachable DR is still a member")
	assert.Equal(t, 2, q.Required)
	assert.Equal(t, 2, q.Online)
	assert.True(t, q.HasQuorum)
	assert.Equal(t, 0, q.Tolerated, "the margin is spent; the next failure stops I/O")
}

// Four members need three, which is why adding a DR to a resource that already
// had a tiebreaker bought nothing: the majority rose in step with the vote.
func TestParseQuorumFourMembersNeedThree(t *testing.T) {
	out := `openclaw node-id:0 role:Primary
      quorum:yes open:yes
  haify-b node-id:1 connection:Connected role:Secondary
  haify-e node-id:2 connection:Connected role:Secondary
  iZ2vca1 node-id:3 connection:Connected role:Secondary
`
	q := parseQuorum(out)
	assert.Equal(t, 4, q.Members)
	assert.Equal(t, 3, q.Required)
	assert.Equal(t, 1, q.Tolerated, "four members tolerate exactly one loss, same as three")
}

// DRBD's own verdict wins over the arithmetic: they disagree transiently while a
// connection comes up, and DRBD is what actually gates I/O.
func TestParseQuorumTakesDRBDVerdictNotArithmetic(t *testing.T) {
	out := `openclaw node-id:0 role:Primary
      quorum:no open:yes blocked:no
  haify-b node-id:1 connection:Connected role:Secondary
  haify-e node-id:2 connection:Connected role:Secondary
`
	q := parseQuorum(out)
	assert.Equal(t, 3, q.Online)
	assert.Equal(t, 2, q.Required)
	assert.False(t, q.HasQuorum, "reported, not recomputed")
}

func TestParseQuorumSingleNode(t *testing.T) {
	q := parseQuorum("data node-id:0 role:Secondary\n      quorum:yes open:no\n")
	assert.Equal(t, 1, q.Members)
	assert.Equal(t, 1, q.Required)
	assert.Equal(t, 1, q.Online)
	assert.Equal(t, 0, q.Tolerated)
}
