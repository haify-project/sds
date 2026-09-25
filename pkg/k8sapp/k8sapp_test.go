package k8sapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func sdsCluster() *Manager {
	return &Manager{kube: fake.NewClientset(
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "local-path"}, Provisioner: "rancher.io/local-path"},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "sds-drbd-remote"}, Provisioner: sdsDriverName,
			Parameters: map[string]string{"allowRemoteVolumeAccess": "true"}},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "sds-drbd"}, Provisioner: sdsDriverName},
	)}
}

func TestMySQLAppFailsOverWithTheVolume(t *testing.T) {
	m := sdsCluster()
	ctx := context.Background()
	got, err := m.Create(ctx, Request{Template: "mysql", Name: "orders", Namespace: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if got.StorageClass != "sds-drbd" {
		t.Errorf("storage class = %q, want the SDS class that keeps data local", got.StorageClass)
	}
	if got.Service != "orders.shop.svc:3306" {
		t.Errorf("service = %q", got.Service)
	}

	dep, err := m.kube.AppsV1().Deployments("shop").Get(ctx, "orders", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// A StatefulSet or a rolling update would keep a second pod from starting
	// while the first is stuck on a dead node.
	if dep.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || *dep.Spec.Replicas != 1 {
		t.Errorf("strategy %s replicas %d, want Recreate with 1", dep.Spec.Strategy.Type, *dep.Spec.Replicas)
	}
	evicts := map[string]int64{}
	for _, tol := range dep.Spec.Template.Spec.Tolerations {
		if tol.TolerationSeconds != nil {
			evicts[tol.Key] = *tol.TolerationSeconds
		}
	}
	for _, k := range []string{"node.kubernetes.io/unreachable", "node.kubernetes.io/not-ready"} {
		if evicts[k] != failoverSeconds {
			t.Errorf("toleration %s = %ds, want %ds", k, evicts[k], failoverSeconds)
		}
	}
	c := dep.Spec.Template.Spec.Containers[0]
	if c.Image != "mysql:8.4" || c.VolumeMounts[0].SubPath != "mysql" {
		t.Errorf("image %s subPath %q", c.Image, c.VolumeMounts[0].SubPath)
	}
	if c.Env[0].ValueFrom.SecretKeyRef.Name != "orders-auth" {
		t.Errorf("password not read from the generated secret")
	}

	pvc, err := m.kube.CoreV1().PersistentVolumeClaims("shop").Get(ctx, "orders-data", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// RWOP would count the pod left Terminating on a dead node and block the
	// replacement.
	if pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce || *pvc.Spec.StorageClassName != "sds-drbd" {
		t.Errorf("claim %v on %s", pvc.Spec.AccessModes, *pvc.Spec.StorageClassName)
	}
	if q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; q.String() != defaultSize {
		t.Errorf("size = %s", q.String())
	}
	sec, _ := m.kube.CoreV1().Secrets("shop").Get(ctx, "orders-auth", metav1.GetOptions{})
	if len(sec.StringData["password"]) < 20 {
		t.Errorf("password too short: %q", sec.StringData["password"])
	}
	if _, err := m.kube.CoreV1().Namespaces().Get(ctx, "shop", metav1.GetOptions{}); err != nil {
		t.Errorf("namespace not created: %v", err)
	}
}

func TestCreateNeverReplacesAnExistingApp(t *testing.T) {
	m := sdsCluster()
	ctx := context.Background()
	if _, err := m.Create(ctx, Request{Template: "mysql"}); err != nil {
		t.Fatal(err)
	}
	before, _ := m.kube.CoreV1().Secrets(defaultNS).Get(ctx, "mysql-auth", metav1.GetOptions{})
	_, err := m.Create(ctx, Request{Template: "mysql"})
	var br *InvalidError
	if !errors.As(err, &br) {
		t.Fatalf("second create: %v, want a bad request", err)
	}
	after, _ := m.kube.CoreV1().Secrets(defaultNS).Get(ctx, "mysql-auth", metav1.GetOptions{})
	if before.StringData["password"] != after.StringData["password"] {
		t.Error("the password of the existing app was replaced")
	}
}

func TestStorageClassMustBeSDS(t *testing.T) {
	m := sdsCluster()
	_, err := m.Create(context.Background(), Request{Template: "postgres", StorageClass: "local-path"})
	if err == nil || !strings.Contains(err.Error(), "not SDS") {
		t.Fatalf("err = %v", err)
	}
	empty := &Manager{kube: fake.NewClientset()}
	if _, err := empty.Create(context.Background(), Request{Template: "mysql"}); err == nil {
		t.Fatal("created an app on a cluster without the SDS driver")
	}
}

func TestRequestValidation(t *testing.T) {
	for _, r := range []Request{
		{Template: "oracle"},
		{Template: "mysql", Name: "My_DB"},
		{Template: "mysql", Size: "lots"},
		{Template: "mysql", Size: "-1Gi"},
	} {
		if _, err := r.normalize(); err == nil {
			t.Errorf("%+v accepted", r)
		}
	}
	r := Request{Template: " Postgres "}
	if _, err := r.normalize(); err != nil || r.Name != "postgres" || r.Namespace != "default" || r.Image != "postgres:17" {
		t.Errorf("defaults: %+v %v", r, err)
	}
}
