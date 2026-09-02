package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	v1 "github.com/liliang-cn/sds/api/proto/v1"
)

// gatewayFixture builds a gateway manager over a resource that is otherwise
// perfectly creatable, so a rejected create can only be the validation under
// test and not an incidental failure further down.
func gatewayFixture(t *testing.T, resource string) (*Manager, *MockResourceManager, *MockDeploymentClient) {
	t.Helper()
	resources := &MockResourceManager{
		Resources: map[string]*ResourceInfo{
			resource: {
				Name:    resource,
				Nodes:   []string{"node1", "node2"},
				Volumes: testVolumes(2),
			},
		},
	}
	deployment := &MockDeploymentClient{}
	return New(resources, deployment, zap.NewNop(), []string{"node1", "node2"}), resources, deployment
}

// assertNoSideEffects is the guarantee the validators exist for: a rejected
// request must not have touched the cluster. Every side effect a gateway create
// performs — the OCF prerequisite probes, the modprobe of the nvmet modules,
// the mkfs of the cluster-private volume, the promoter config distribution —
// goes through one of these two mock recorders, so both being empty means the
// caller can retry a corrected request against an untouched cluster.
func assertNoSideEffects(t *testing.T, deployment *MockDeploymentClient) {
	t.Helper()
	assert.Empty(t, deployment.ExecCommands, "a rejected create must not run commands on any node")
	assert.Empty(t, deployment.Configs, "a rejected create must not write a promoter config")
}

func TestCreateNVMeGatewayRejectsBadTransportBeforeSideEffects(t *testing.T) {
	// The motivating typo: "rmda" reached the nvmet-port OCF agent verbatim and
	// only failed when drbd-reactor tried to promote the finished gateway.
	base, _, deployment := gatewayFixture(t, "data")
	nvme := NewNVMeManager(base)

	resp, err := nvme.CreateNVMeGateway(context.Background(), &v1.CreateNVMeGatewayRequest{
		Resource:      "data",
		Nqn:           "nqn.2024-01.com.example:sds.data",
		ServiceIp:     "192.168.1.150/24",
		TransportType: "rmda",
	})

	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "invalid transport type")
	require.NotNil(t, resp)
	assert.False(t, resp.Success)
	assertNoSideEffects(t, deployment)
}

func TestCreateNVMeGatewayRejectsBadNQNBeforeSideEffects(t *testing.T) {
	base, _, deployment := gatewayFixture(t, "data")
	nvme := NewNVMeManager(base)

	resp, err := nvme.CreateNVMeGateway(context.Background(), &v1.CreateNVMeGatewayRequest{
		Resource:  "data",
		Nqn:       "nqn-missing-colon",
		ServiceIp: "192.168.1.150/24",
	})

	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	require.NotNil(t, resp)
	assert.False(t, resp.Success)
	assertNoSideEffects(t, deployment)
}

// An unset transport is the documented way to ask for the tcp default, so it
// must survive validation rather than being read as a caller mistake.
func TestCreateNVMeGatewayAcceptsEmptyTransport(t *testing.T) {
	base, _, deployment := gatewayFixture(t, "data")
	nvme := NewNVMeManager(base)

	resp, err := nvme.CreateNVMeGateway(context.Background(), &v1.CreateNVMeGatewayRequest{
		Resource:  "data",
		Nqn:       "nqn.2024-01.com.example:sds.data",
		ServiceIp: "192.168.1.150/24",
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Success)
	assert.Contains(t, deployment.Configs[gatewayConfigPath("sds-nvmeof-data")], "type=tcp")
}

func TestCreateISCSIGatewayRejectsBadIQNBeforeSideEffects(t *testing.T) {
	base, _, deployment := gatewayFixture(t, "data")
	iscsi := NewISCSIManager(base)

	resp, err := iscsi.CreateISCSIGateway(context.Background(), &v1.CreateISCSIGatewayRequest{
		Resource:  "data",
		Iqn:       "iqn.no-colon-here",
		ServiceIp: "192.168.1.100/24",
	})

	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	require.NotNil(t, resp)
	assert.False(t, resp.Success)
	assertNoSideEffects(t, deployment)
}

// A bad service IP is the caller's mistake too, and used to reach them as an
// unclassified error indistinguishable from an unreachable node.
func TestCreateGatewayServiceIPErrorIsInvalidArgument(t *testing.T) {
	base, _, _ := gatewayFixture(t, "data")

	_, nfsErr := NewNFSManager(base).CreateNFSGateway(context.Background(), &v1.CreateNFSGatewayRequest{
		Resource:   "data",
		ServiceIp:  "not-an-ip",
		ExportPath: "/data",
	})
	require.Error(t, nfsErr)
	assert.Equal(t, codes.InvalidArgument, status.Code(nfsErr))

	_, iscsiErr := NewISCSIManager(base).CreateISCSIGateway(context.Background(), &v1.CreateISCSIGatewayRequest{
		Resource:  "data",
		Iqn:       "iqn.2024-01.com.example:sds.data",
		ServiceIp: "not-an-ip",
	})
	require.Error(t, iscsiErr)
	assert.Equal(t, codes.InvalidArgument, status.Code(iscsiErr))

	_, nvmeErr := NewNVMeManager(base).CreateNVMeGateway(context.Background(), &v1.CreateNVMeGatewayRequest{
		Resource:  "data",
		Nqn:       "nqn.2024-01.com.example:sds.data",
		ServiceIp: "not-an-ip",
	})
	require.Error(t, nvmeErr)
	assert.Equal(t, codes.InvalidArgument, status.Code(nvmeErr))
}

// TestValidateIQN pins all three RFC 3720 name formats. The iqn. rows are the
// ones this validator shipped with and must keep answering identically; the
// eui./naa. rows are the regression the widening fixed — LIO accepts those
// names, so a gateway created with one used to work and started failing the
// moment the (previously dead) validator was wired into the create path.
func TestValidateIQN(t *testing.T) {
	tests := []struct {
		name     string
		iqn      string
		hasError bool
	}{
		// iqn. — unchanged rules.
		{"iqn plain", "iqn.2024-01.com.example:storage", false},
		{"iqn dotted local name", "iqn.2024-01.com.example:sds.data", false},
		{"iqn missing colon", "iqn.invalid", true},

		// eui. — exactly 16 hex digits.
		{"eui lowercase", "eui.0123456789abcdef", false},
		{"eui uppercase hex", "eui.0123456789ABCDEF", false},
		{"eui all f", "eui.ffffffffffffffff", false},
		{"eui too short", "eui.0123456789abcde", true},
		{"eui too long", "eui.0123456789abcdef0", true},
		{"eui non-hex digit", "eui.0123456789abcdeg", true},
		{"eui prefix only", "eui.", true},

		// naa. — 16 or 32 hex digits.
		{"naa 16 digits", "naa.60014051f1b2c3d4", false},
		{"naa 32 digits", "naa.60014051f1b2c3d4e5f60718293a4b5c", false},
		{"naa uppercase hex", "naa.60014051F1B2C3D4", false},
		{"naa 24 digits is neither length", "naa.60014051f1b2c3d4e5f60718", true},
		{"naa 31 digits", "naa.60014051f1b2c3d4e5f60718293a4b5", true},
		{"naa non-hex digit", "naa.60014051f1b2c3dz", true},
		{"naa prefix only", "naa.", true},

		// Neither a known prefix nor a bare identifier.
		{"nqn prefix", "nqn.2024-01.com.example:storage", true},
		{"unprefixed hex", "0123456789abcdef", true},
		{"uppercase prefix", "IQN.2024-01.com.example:storage", true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateIQN(tt.iqn)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// A rejection has to tell the operator which spellings exist, not merely that
// theirs is wrong: the failure this validator is most likely to produce is a
// perfectly legal name in a format the caller did not realise was an option.
func TestValidateIQNErrorNamesEveryAcceptedFormat(t *testing.T) {
	for _, bad := range []string{"", "not-a-name", "eui.abc", "naa.60014051f1b2c3dz", "iqn.invalid"} {
		err := validateIQN(bad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "iqn.", "%q: error must mention the iqn. format", bad)
		assert.Contains(t, err.Error(), "eui.", "%q: error must mention the eui. format", bad)
		assert.Contains(t, err.Error(), "naa.", "%q: error must mention the naa. format", bad)
	}
}

// The gateway create path is the only caller, so the widening is only real if a
// eui.-named target survives it — that is the request that regressed.
func TestCreateISCSIGatewayAcceptsEUIAndNAANames(t *testing.T) {
	for _, iqn := range []string{"eui.0123456789abcdef", "naa.60014051f1b2c3d4"} {
		t.Run(iqn, func(t *testing.T) {
			base, _, deployment := gatewayFixture(t, "data")

			resp, err := NewISCSIManager(base).CreateISCSIGateway(context.Background(), &v1.CreateISCSIGatewayRequest{
				Resource:  "data",
				Iqn:       iqn,
				ServiceIp: "192.168.1.100/24",
			})

			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.True(t, resp.Success)
			assert.Contains(t, deployment.Configs[gatewayConfigPath("sds-iscsi-data")], "iqn="+iqn)
		})
	}
}

func TestValidateNQN(t *testing.T) {
	tests := []struct {
		nqn      string
		hasError bool
	}{
		{"nqn.2024-01.com.example:storage", false},
		{"nqn.2024-01.com.example:sds.data", false},
		{"iqn.2024-01.com.example:storage", true}, // Wrong prefix
		{"nqn.invalid", true},                     // Missing colon
		{"", true},                                // Empty
	}

	for _, tt := range tests {
		t.Run(tt.nqn, func(t *testing.T) {
			err := validateNQN(tt.nqn)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestParseTransportType(t *testing.T) {
	tests := []struct {
		transport string
		hasError  bool
	}{
		{"tcp", false},
		{"rdma", false},
		{"fc", false},
		{"invalid", true},
		{"", true},
	}

	for _, tt := range tests {
		t.Run(tt.transport, func(t *testing.T) {
			err := parseTransportType(tt.transport)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
