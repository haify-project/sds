package gateway

import (
	"context"
	"strings"
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

// TestValidateIQN pins the names LIO (rtslib normalize_wwn for the iSCSI
// fabric) accepts. A name it rejects is an outage at the next target start,
// so the iqn./naa. rows follow rtslib rather than RFC 3720.
func TestValidateIQN(t *testing.T) {
	tests := []struct {
		name     string
		iqn      string
		hasError bool
	}{
		{"iqn plain", "iqn.2024-01.com.example:storage", false},
		{"iqn dotted local name", "iqn.2024-01.com.example:sds.data", false},
		{"iqn two-label domain", "iqn.2026-10.lab.test:probe", false},
		{"iqn without local name", "iqn.2024-01.com.example", false},
		{"iqn one-label domain (sdt outage)", "iqn.2026-10.test:probe", true},
		{"iqn no date", "iqn.invalid", true},
		{"iqn month 2x", "iqn.2024-21.com.example:x", true},
		{"iqn underscore", "iqn.2024-01.com.example:sds_data", true},
		{"iqn space", "iqn.2024-01.com.example:sds data", true},
		{"iqn quote", "iqn.2024-01.com.example:sds\"data", true},

		{"eui lowercase", "eui.0123456789abcdef", false},
		{"eui uppercase hex", "eui.0123456789ABCDEF", false},
		{"eui too short", "eui.0123456789abcde", true},
		{"eui too long", "eui.0123456789abcdef0", true},
		{"eui non-hex digit", "eui.0123456789abcdeg", true},
		{"eui prefix only", "eui.", true},

		{"naa 16 digits", "naa.5001405f1b2c3d4e", false},
		{"naa uppercase hex", "naa.5001405F1B2C3D4E", false},
		{"naa type 6 first digit", "naa.60014051f1b2c3d4", true},
		{"naa 32 digits", "naa.60014051f1b2c3d4e5f60718293a4b5c", true},
		{"naa non-hex digit", "naa.5001405f1b2c3d4z", true},
		{"naa prefix only", "naa.", true},

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
	for _, bad := range []string{"", "not-a-name", "eui.abc", "naa.5001405f1b2c3d4z", "iqn.invalid"} {
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
	for _, iqn := range []string{"eui.0123456789abcdef", "naa.5001405f1b2c3d4e"} {
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
		{"fc", true},
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

func TestValidateHostNQN(t *testing.T) {
	for nqn, bad := range map[string]bool{
		"nqn.2024-01.com.example:host1":                                        false,
		"nqn.2014-08.org.nvmexpress:uuid:3c1e4a0e-8f2b-4c6d-9e1a-2b3c4d5e6f70": false,
		"nqn.2026-10.lab.test":                                                 false,
		"nqn.2014-08.org.nvmexpress:uuid:not-a-uuid":                           true,
		"nqn.2026-10.test:probe":                                               true,
		"nqn.present":                                                          true,
		"nqn.2024-13.com.example:x":                                            true,
		"nqn.2024-01.com.example:a b":                                          true,
		"nqn.2024-01.com.example:a/b":                                          true,
		"iqn.2024-01.com.example:host1":                                        true,
		"nqn.2024-01.com.example:" + strings.Repeat("x", 220):                  true,
	} {
		err := validateHostNQN(nqn)
		assert.Equal(t, bad, err != nil, "%q: %v", nqn, err)
	}
}

// An initiator or host LIO/nvmet would refuse is rejected before any node is
// touched, as InvalidArgument.
func TestEditsRejectInvalidInitiatorNames(t *testing.T) {
	dep := &MockDeploymentClient{}
	m := New(nil, dep, zap.NewNop(), []string{"node1"})
	err := NewISCSIManager(m).AddInitiator(context.Background(), "res", "iqn.2026-10.test:probe")
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	err = NewNVMeManager(m).AddHost(context.Background(), "res", "nqn.present")
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Empty(t, dep.ExecCommands, "nothing ran on any node")

	_, err = NewISCSIManager(m).CreateISCSIGateway(context.Background(), &v1.CreateISCSIGatewayRequest{
		Resource: "res", Iqn: "iqn.2024-01.com.example:res", ServiceIp: "192.168.1.100/24",
		AllowedInitiators: []string{"iqn.2026-10.lab.test:ok", "iqn.2026-10.test:probe"},
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "iqn.2026-10.test:probe")
}
