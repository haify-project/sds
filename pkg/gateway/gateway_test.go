package gateway

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestParseServiceIP(t *testing.T) {
	tests := []struct {
		input       string
		expectedIP  string
		expectedLen int
		hasError    bool
	}{
		{"192.168.1.100/24", "192.168.1.100", 24, false},
		{"10.0.0.50/16", "10.0.0.50", 16, false},
		{"172.16.0.1/32", "172.16.0.1", 32, false},
		{"192.168.1.100", "", 0, true}, // No CIDR
		{"invalid", "", 0, true},
		{"", "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := parseServiceIP(tt.input)
			if tt.hasError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, tt.expectedIP, result.IP.String())
				assert.Equal(t, tt.expectedLen, result.Prefix)
			}
		})
	}
}

func TestExtractNodeName(t *testing.T) {
	tests := []struct {
		endpoint string
		expected string
	}{
		{"orange1:50051", "orange1"},
		{"192.168.1.100:3374", "192.168.1.100"},
		{"localhost:8080", "localhost"},
		{"node-only", "node-only"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			result := extractNodeName(tt.endpoint)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExecuteTemplate(t *testing.T) {
	tmpl := `Name: {{ .Name }}, Value: {{ .Value }}`
	data := struct {
		Name  string
		Value int
	}{
		Name:  "test",
		Value: 42,
	}

	result, err := executeTemplate(tmpl, data)
	require.NoError(t, err)
	assert.Equal(t, "Name: test, Value: 42", result)
}

func TestExecuteTemplateWithConditionals(t *testing.T) {
	tmpl := `{{ if .Show }}Name: {{ .Name }}{{ end }}`
	data := struct {
		Name string
		Show bool
	}{
		Name: "test",
		Show: true,
	}

	result, err := executeTemplate(tmpl, data)
	require.NoError(t, err)
	assert.Contains(t, result, "Name: test")

	// Test with Show = false
	data.Show = false
	result, err = executeTemplate(tmpl, data)
	require.NoError(t, err)
	assert.NotContains(t, result, "Name")
}

// ==================== Manager Tests ====================

func TestNewManager(t *testing.T) {
	logger := zap.NewNop()
	mockResources := &MockResourceManager{}
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1", "node2"}

	manager := New(mockResources, mockDeployment, logger, hosts)

	require.NotNil(t, manager)
	assert.Equal(t, logger, manager.logger)
	assert.Equal(t, hosts, manager.hosts)
}

// ==================== Mock Implementations ====================

type MockResourceManager struct {
	Resources      map[string]*ResourceInfo
	SetPrimaryFunc func(ctx context.Context, resource, node string, force bool) error
}

func (m *MockResourceManager) GetResource(ctx context.Context, name string) (*ResourceInfo, error) {
	if m.Resources == nil {
		return nil, fmt.Errorf("resource not found")
	}
	res, ok := m.Resources[name]
	if !ok {
		return nil, fmt.Errorf("resource not found")
	}
	return res, nil
}

func (m *MockResourceManager) SetPrimary(ctx context.Context, resource, node string, force bool) error {
	if m.SetPrimaryFunc != nil {
		return m.SetPrimaryFunc(ctx, resource, node, force)
	}
	return nil
}

// EnsureGatewayVolumesFunc lets tests stub auto-provisioning; nil is a no-op so
// the existing volume-count assertions keep working unchanged.
func (m *MockResourceManager) EnsureGatewayVolumes(ctx context.Context, resource string, minVolumes int) error {
	return nil
}

type MockDeploymentClient struct {
	Configs      map[string]string
	ExecCommands []string
	// ExecHosts[i] is the host list ExecCommands[i] ran on; ConfigHosts maps a
	// distributed path to the hosts it went to.
	ExecHosts     [][]string
	ConfigHosts   map[string][]string
	DistributeErr error
	ExecErr       error
	// HostOutputs is what ExecOutput reports per host; a host absent from it
	// did not answer.
	HostOutputs map[string]string
	// NodeConfigs is what each node holds in /etc/drbd-reactor.d, by path,
	// for config dump scripts; a host absent from it did not answer. When it
	// is nil every node holds Configs.
	NodeConfigs map[string]map[string]string
	// TargetStates is what systemctl is-active says of a gateway's services
	// target, per host; a host absent from it did not answer. When it is nil
	// every host reports "inactive": the gateway runs nowhere.
	TargetStates map[string]string
	// ScriptErr fails a base64-wrapped script when the decoded script
	// contains the key.
	ScriptErr map[string]error
}

func (m *MockDeploymentClient) ExecOutput(ctx context.Context, hosts []string, cmd string) (map[string]string, error) {
	if m.ExecErr != nil {
		return nil, m.ExecErr
	}
	m.ExecCommands = append(m.ExecCommands, cmd)
	m.ExecHosts = append(m.ExecHosts, append([]string(nil), hosts...))
	if patterns := dumpScriptPatterns(cmd); patterns != nil {
		return m.dumpConfigs(hosts, patterns), nil
	}
	if strings.Contains(decodeScriptCmd(cmd), targetStateMarker) {
		out := map[string]string{}
		for _, h := range hosts {
			state, ok := "inactive", true
			if m.TargetStates != nil {
				state, ok = m.TargetStates[h]
			}
			if ok {
				out[h] = targetStateMarker + " " + state + "\n"
			}
		}
		return out, nil
	}
	out := map[string]string{}
	for _, h := range hosts {
		if v, ok := m.HostOutputs[h]; ok {
			out[h] = v
		}
	}
	return out, nil
}

func (m *MockDeploymentClient) GetConfig(path string) (string, bool) {
	if m.Configs == nil {
		return "", false
	}
	value, ok := m.Configs[path]
	return value, ok
}

func (m *MockDeploymentClient) SetConfig(path, content string) {
	if m.Configs == nil {
		m.Configs = make(map[string]string)
	}
	m.Configs[path] = content
}

func (m *MockDeploymentClient) DistributeConfig(ctx context.Context, hosts []string, content, remotePath string) error {
	if m.DistributeErr != nil {
		return m.DistributeErr
	}
	if m.Configs == nil {
		m.Configs = make(map[string]string)
	}
	m.Configs[remotePath] = content
	if m.ConfigHosts == nil {
		m.ConfigHosts = make(map[string][]string)
	}
	m.ConfigHosts[remotePath] = append([]string(nil), hosts...)
	return nil
}

func (m *MockDeploymentClient) Exec(ctx context.Context, hosts []string, cmd string) error {
	if m.ExecErr != nil {
		return m.ExecErr
	}
	m.ExecCommands = append(m.ExecCommands, cmd)
	m.ExecHosts = append(m.ExecHosts, append([]string(nil), hosts...))
	script := decodeScriptCmd(cmd)
	for key, err := range m.ScriptErr {
		if script != "" && strings.Contains(script, key) {
			return err
		}
	}
	return nil
}

// decodeScriptCmd returns the script a scriptCmd command runs, or "".
func decodeScriptCmd(cmd string) string {
	m := scriptCmdRE.FindStringSubmatch(cmd)
	if m == nil {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		return ""
	}
	return string(raw)
}

// ==================== ServiceIP Tests ====================

func TestServiceIPStruct(t *testing.T) {
	ip := net.ParseIP("192.168.1.100")
	serviceIP := &ServiceIP{
		IP:     ip,
		Prefix: 24,
	}

	assert.Equal(t, "192.168.1.100", serviceIP.IP.String())
	assert.Equal(t, 24, serviceIP.Prefix)
}

// testVolumes builds consecutive-minor volumes matching the old fixture
// assumption (/dev/drbd0 base, volume N at minor N).
func testVolumes(count int) []*ResourceVolumeInfo {
	vols := make([]*ResourceVolumeInfo, count)
	for i := 0; i < count; i++ {
		vols[i] = &ResourceVolumeInfo{VolumeID: uint32(i), Device: fmt.Sprintf("/dev/drbd%d", i), SizeGB: 1}
	}
	return vols
}
