package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/database"
)

func TestSetResourceLabels(t *testing.T) {
	ctx := context.Background()
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	srv := &Server{ctrl: ctrl, resources: ctrl.resources}
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{
		Name: "web1", Nodes: "k1,k2", Labels: map[string]string{"team": "a", "old": "x"},
	}))

	resp, err := srv.SetResourceLabels(ctx, &haifypb.SetResourceLabelsRequest{
		Resource: "web1",
		Labels:   map[string]string{"haify.libvirt/domain": "web1", "team": "b", "old": ""},
	})
	require.NoError(t, err)
	want := map[string]string{"haify.libvirt/domain": "web1", "team": "b"}
	assert.Equal(t, want, resp.Labels)
	assert.Equal(t, "labels of web1: haify.libvirt/domain=web1,team=b", resp.Message)
	stored, err := ctrl.db.GetResource(ctx, "web1")
	require.NoError(t, err)
	assert.Equal(t, want, stored.Labels, "the labels are kept with the resource")

	// k=v lists are joined with commas, so neither side may carry one.
	for _, labels := range []map[string]string{
		{"a,b": "x"}, {"a=b": "x"}, {"": "x"}, {"k": "a,b"}, {"k": "a b"},
	} {
		_, err := srv.SetResourceLabels(ctx, &haifypb.SetResourceLabelsRequest{Resource: "web1", Labels: labels})
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "%v", labels)
	}

	_, err = srv.SetResourceLabels(ctx, &haifypb.SetResourceLabelsRequest{Resource: "nope", Labels: want})
	assert.Equal(t, codes.NotFound, status.Code(err))
}
