package deployment

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseReactorStatusJSON(t *testing.T) {
	rawJSON := `{
		"promoter": [
			{
				"drbd_resource": "res1",
				"path": "/etc/drbd-reactor.d/res1.toml",
				"primary_on": "node1",
				"status": "active",
				"target": {
					"name": "drbd-services@res1.target",
					"status": "active",
					"freezer": "running"
				},
				"dependencies": [
					{
						"name": "var-lib-res1.mount",
						"status": "active"
					}
				]
			}
		]
	}`

	var status ReactorStatus
	err := json.Unmarshal([]byte(rawJSON), &status)
	require.NoError(t, err)
	assert.Len(t, status.Promoter, 1)
	assert.Equal(t, "res1", status.Promoter[0].DRBDResource)
	assert.Equal(t, "node1", status.Promoter[0].PrimaryOn)
	assert.Equal(t, "active", status.Promoter[0].Status)
	assert.Equal(t, "drbd-services@res1.target", status.Promoter[0].Target.Name)
	assert.Len(t, status.Promoter[0].Dependencies, 1)
	assert.Equal(t, "var-lib-res1.mount", status.Promoter[0].Dependencies[0].Name)
}
