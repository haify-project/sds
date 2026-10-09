package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/haify-project/haify/pkg/k8sapp"
)

func haifyKube() *fake.Clientset {
	return fake.NewClientset(&storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "haify-drbd"},
		Provisioner: "haify.csi.liliang-cn.com",
	})
}

// The bare-metal server carries no Kubernetes tools, and the haify-k8s server
// nothing but.
func TestKubernetesToolsAreTheirOwnServer(t *testing.T) {
	bare := toolNames(t, New(&mockExtraClient{}, zap.NewNop(), Options{}))
	for name := range bare {
		assert.NotContains(t, name, "haify_k8s_")
	}
	k8s := toolNames(t, NewK8s(k8sapp.NewManagerFor(haifyKube()), zap.NewNop(), Options{}))
	assert.Equal(t, map[string]bool{"haify_k8s_app_list": true, "haify_k8s_app_create": true, "haify_k8s_app_delete": true}, k8s)

	ro := toolNames(t, NewK8s(k8sapp.NewManagerFor(haifyKube()), zap.NewNop(), Options{ReadOnly: true}))
	assert.Equal(t, map[string]bool{"haify_k8s_app_list": true}, ro)
}

func TestAppCreateThroughTheTool(t *testing.T) {
	kube := haifyKube()
	s := NewK8s(k8sapp.NewManagerFor(kube), zap.NewNop(), Options{})
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := s.MCPServer().Connect(ctx, st, nil)
	require.NoError(t, err)
	defer func() { _ = ss.Close() }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	defer func() { _ = session.Close() }()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "haify_k8s_app_create",
		Arguments: map[string]any{"template": "mysql", "name": "orders", "size": "2Gi"}})
	require.NoError(t, err)
	require.False(t, res.IsError, "%v", res.Content)

	dep, err := kube.AppsV1().Deployments("default").Get(ctx, "orders", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "mysql:8.4", dep.Spec.Template.Spec.Containers[0].Image)

	// A second create is refused rather than replacing the app.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "haify_k8s_app_create",
		Arguments: map[string]any{"template": "mysql", "name": "orders"}})
	require.NoError(t, err)
	assert.True(t, res.IsError)

	// Deleted without delete_data, the data stays for the next create.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "haify_k8s_app_delete",
		Arguments: map[string]any{"name": "orders"}})
	require.NoError(t, err)
	require.False(t, res.IsError, "%v", res.Content)
	_, err = kube.AppsV1().Deployments("default").Get(ctx, "orders", metav1.GetOptions{})
	assert.Error(t, err)
	_, err = kube.CoreV1().PersistentVolumeClaims("default").Get(ctx, "orders-data", metav1.GetOptions{})
	assert.NoError(t, err)
}
