package controller

import (
	"testing"

	"github.com/haify-project/haify/pkg/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMaxPlaceableSizeIsTheSmallestOfTheBestReplicaSet(t *testing.T) {
	cands := []placementNode{
		{node: "a", freeGB: 100, freeBytes: 100 << 30, labels: map[string]string{"rack": "1"}},
		{node: "b", freeGB: 80, freeBytes: 80 << 30, labels: map[string]string{"rack": "1"}},
		{node: "c", freeGB: 30, freeBytes: 30<<30 - 1, labels: map[string]string{"rack": "2"}},
	}
	size, picked := maxPlaceableSize(cands, 2, placementConstraints{})
	assert.Equal(t, uint64(80), size)
	assert.ElementsMatch(t, []string{"a", "b"}, picked)

	// Spread across racks: a and b share one, so the second copy must go to c,
	// which is a byte short of 30 GiB — the answer rounds down, never up.
	size, picked = maxPlaceableSize(cands, 2, placementConstraints{onDifferent: []string{"rack"}})
	assert.Equal(t, uint64(29), size)
	assert.Contains(t, picked, "c")

	size, picked = maxPlaceableSize(cands, 4, placementConstraints{})
	assert.Zero(t, size)
	assert.Nil(t, picked)
}

// A new replica joins replicas that are already placed: the constraints hold
// across the whole set, not only among the new ones.
func TestFitsExistingReplicas(t *testing.T) {
	labels := map[string]map[string]string{
		"a": {"rack": "1", "zone": "east"},
		"b": {"rack": "2", "zone": "east"},
	}
	existing := []string{"a", "b"}
	assert.False(t, fitsExistingReplicas(map[string]string{"rack": "1", "zone": "east"}, existing, labels, []string{"rack"}, nil),
		"rack 1 already holds a replica")
	assert.True(t, fitsExistingReplicas(map[string]string{"rack": "3", "zone": "east"}, existing, labels, []string{"rack"}, []string{"zone"}))
	assert.False(t, fitsExistingReplicas(map[string]string{"rack": "3", "zone": "west"}, existing, labels, nil, []string{"zone"}),
		"the replicas are all in east")
}

func TestProfileMembersAndAssignment(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	ctx := t.Context()
	require.NoError(t, ctrl.db.SaveResourceProfile(ctx, dbProfile("db")))
	for _, r := range []struct{ name, profile string }{{"r1", "db"}, {"r2", ""}, {"r3", "db"}} {
		require.NoError(t, ctrl.db.SaveResource(ctx, dbResource(r.name, r.profile)))
	}
	members, err := ctrl.resources.profileMembers(ctx, "db")
	require.NoError(t, err)
	require.Len(t, members, 2)
	assert.Equal(t, "r1", members[0].Name)

	require.NoError(t, ctrl.resources.AssignProfile(ctx, "r2", "db"))
	members, _ = ctrl.resources.profileMembers(ctx, "db")
	assert.Len(t, members, 3)
	require.NoError(t, ctrl.resources.AssignProfile(ctx, "r1", ""))
	members, _ = ctrl.resources.profileMembers(ctx, "db")
	assert.Len(t, members, 2)

	assert.Error(t, ctrl.resources.AssignProfile(ctx, "r2", "nope"))
}

// Adjust never removes a replica, and a dry run changes nothing.
func TestAdjustMemberReportsWithoutRemoving(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	ctx := t.Context()
	p := dbProfile("db")
	p.Replicas = 2
	r := dbResource("r1", "db")
	r.Nodes = "a,b,c"
	res := ctrl.resources.adjustMember(ctx, p, r, true)
	assert.True(t, res.OK)
	assert.Contains(t, res.Message, "none removed")
}

func dbProfile(name string) *database.ResourceProfile {
	return &database.ResourceProfile{Name: name, Pool: "tp"}
}

func dbResource(name, profile string) *database.Resource {
	return &database.Resource{Name: name, Nodes: "a,b", Port: 7000, Profile: profile}
}
