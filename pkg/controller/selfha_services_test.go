package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/haify/pkg/config"
	"github.com/haify-project/haify/pkg/database"
)

func TestSelfHaServicesFollowExtraServices(t *testing.T) {
	ctx := context.Background()
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	if ctrl.config == nil {
		ctrl.config = &config.Config{}
	}
	require.NoError(t, ctrl.db.SaveHaConfig(ctx, &database.HaConfig{
		Resource: SelfHaResource, VIP: "10.0.0.250/24", Services: []string{selfHaControllerSvc},
	}))

	// Nothing configured: the stored list is left alone.
	ctrl.resources.reconcileSelfHaServices(ctx)
	cfg, err := ctrl.db.GetHaConfig(ctx, SelfHaResource)
	require.NoError(t, err)
	assert.Equal(t, []string{selfHaControllerSvc}, cfg.Services)

	// Units added to [self_ha] after Self-HA was enabled reach the stored config.
	ctrl.config.SelfHA.ExtraServices = []string{"haify-ai.service", "haify-mcp-http.service"}
	ctrl.resources.reconcileSelfHaServices(ctx)
	cfg, err = ctrl.db.GetHaConfig(ctx, SelfHaResource)
	require.NoError(t, err)
	assert.Equal(t, []string{selfHaControllerSvc, "haify-ai.service", "haify-mcp-http.service"}, cfg.Services)
	assert.Equal(t, "10.0.0.250/24", cfg.VIP, "the rest of the config is kept")
}
