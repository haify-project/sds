package k8sapp

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDeleteKeepsTheDataUnlessAsked(t *testing.T) {
	m := sdsCluster()
	ctx := context.Background()
	_, err := m.Create(ctx, Request{Template: "postgres", Name: "orders", Namespace: "shop"})
	require.NoError(t, err)
	secret, err := m.kube.CoreV1().Secrets("shop").Get(ctx, "orders-auth", metav1.GetOptions{})
	require.NoError(t, err)

	got, err := m.Delete(ctx, DeleteRequest{Namespace: "shop", Name: "orders"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"deployment orders", "service orders"}, got.Removed)
	assert.Contains(t, got.Message, "are kept")
	_, err = m.kube.AppsV1().Deployments("shop").Get(ctx, "orders", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
	_, err = m.kube.CoreV1().PersistentVolumeClaims("shop").Get(ctx, "orders-data", metav1.GetOptions{})
	require.NoError(t, err, "the data stays")

	// Created again, it runs on the kept claim with the kept password.
	again, err := m.Create(ctx, Request{Template: "postgres", Name: "orders", Namespace: "shop"})
	require.NoError(t, err)
	assert.Contains(t, again.Message, "kept claim orders-data")
	kept, err := m.kube.CoreV1().Secrets("shop").Get(ctx, "orders-auth", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, secret.StringData["password"], kept.StringData["password"])

	// A different template does not take another app's data.
	_, err = m.Delete(ctx, DeleteRequest{Namespace: "shop", Name: "orders"})
	require.NoError(t, err)
	_, err = m.Create(ctx, Request{Template: "mysql", Name: "orders", Namespace: "shop"})
	var invalid *InvalidError
	require.True(t, errors.As(err, &invalid), "%v", err)

	got, err = m.Delete(ctx, DeleteRequest{Namespace: "shop", Name: "orders", DeleteData: true})
	if err == nil {
		t.Fatalf("the deployment is already gone, want an error, got %+v", got)
	}
	_, err = m.Create(ctx, Request{Template: "postgres", Name: "orders", Namespace: "shop"})
	require.NoError(t, err)
	got, err = m.Delete(ctx, DeleteRequest{Namespace: "shop", Name: "orders", DeleteData: true})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"deployment orders", "service orders", "claim orders-data", "secret orders-auth"}, got.Removed)
	_, err = m.kube.CoreV1().PersistentVolumeClaims("shop").Get(ctx, "orders-data", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
	_, err = m.kube.CoreV1().Secrets("shop").Get(ctx, "orders-auth", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestDeleteRefusesWhatSDSDidNotCreate(t *testing.T) {
	m := sdsCluster()
	ctx := context.Background()
	_, err := m.kube.AppsV1().Deployments("default").Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"}}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = m.Delete(ctx, DeleteRequest{Name: "web", DeleteData: true})
	var invalid *InvalidError
	require.True(t, errors.As(err, &invalid), "%v", err)
	_, err = m.kube.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{})
	assert.NoError(t, err, "someone else's deployment is left alone")

	_, err = m.Delete(ctx, DeleteRequest{Name: "missing"})
	require.True(t, errors.As(err, &invalid))
	_, err = m.Delete(ctx, DeleteRequest{Name: "Bad_Name"})
	require.True(t, errors.As(err, &invalid))

	// An unlabelled claim of the same name is neither deleted nor adopted.
	_, err = m.Create(ctx, Request{Template: "mysql", Name: "db"})
	require.NoError(t, err)
	require.NoError(t, m.kube.CoreV1().PersistentVolumeClaims("default").Delete(ctx, "db-data", metav1.DeleteOptions{}))
	_, err = m.kube.CoreV1().PersistentVolumeClaims("default").Create(ctx, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "db-data", Namespace: "default"}}, metav1.CreateOptions{})
	require.NoError(t, err)
	got, err := m.Delete(ctx, DeleteRequest{Name: "db", DeleteData: true})
	require.NoError(t, err)
	assert.Contains(t, got.Kept, "claim db-data (not created by Haify)")
	_, err = m.kube.CoreV1().PersistentVolumeClaims("default").Get(ctx, "db-data", metav1.GetOptions{})
	assert.NoError(t, err)
}
