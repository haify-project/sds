// Package k8sapp creates databases on Kubernetes whose data lives on an SDS
// volume. sds-mcp exposes it as the sds_app_create / sds_app_list tools.
package k8sapp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// "I want an HA MySQL" becomes one tool call: a database on Kubernetes whose
// data lives on an SDS volume.
//
// High availability here comes from the storage, not from database
// replication. The volume is a DRBD resource with a replica on two nodes and a
// tiebreaker on a third; the database runs as a single pod. When its node
// dies, Kubernetes evicts the pod after failoverSeconds, schedules the
// replacement on the other replica node (the PV's node affinity allows nothing
// else), and the CSI driver promotes DRBD there. The committed data is already
// on that node — DRBD protocol C acknowledged every write on both. A node that
// is only cut off, not dead, loses quorum and freezes its I/O, so the two pods
// cannot both write.
//
// The shape of the workload follows from that:
//   - a Deployment with strategy Recreate, not a StatefulSet. A StatefulSet
//     never replaces a pod stuck on an unreachable node (it guarantees at most
//     one pod per identity, and cannot prove the old one is gone), so the
//     database would stay down until someone force-deleted it. DRBD quorum is
//     the fence that makes replacing it safe.
//   - ReadWriteOnce, not ReadWriteOncePod. The pod left on a dead node stays
//     Terminating and still counts as the one pod RWOP allows, which would
//     block the replacement exactly when it is needed.
//   - tolerations that evict after failoverSeconds instead of the default five
//     minutes.

const (
	managedByKey    = "app.kubernetes.io/managed-by"
	managedByValue  = "sds"
	templateKey     = "sds.io/app-template"
	sdsDriverName   = "sds.csi.liliang-cn.com"
	failoverSeconds = int64(30)
	defaultSize     = "5Gi"
	defaultNS       = "default"
)

// template describes one application that can be created.
type template struct {
	image     string
	port      int32
	dataDir   string
	secretEnv string // env var the generated password is passed in
	extraEnv  []corev1.EnvVar
	probe     []string
}

var templates = map[string]template{
	"mysql": {
		image:     "mysql:8.4",
		port:      3306,
		dataDir:   "/var/lib/mysql",
		secretEnv: "MYSQL_ROOT_PASSWORD",
		probe:     []string{"mysqladmin", "ping", "-h", "127.0.0.1", "--silent"},
	},
	"postgres": {
		image:     "postgres:17",
		port:      5432,
		dataDir:   "/var/lib/postgresql/data",
		secretEnv: "POSTGRES_PASSWORD",
		// The image refuses a data directory that is a mount point with
		// lost+found in it; PGDATA one level down avoids that.
		extraEnv: []corev1.EnvVar{{Name: "PGDATA", Value: "/var/lib/postgresql/data/pgdata"}},
		probe:    []string{"pg_isready", "-h", "127.0.0.1"},
	},
}

// Request asks for one app. Only Template is required.
type Request struct {
	Template     string `json:"template"`
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Size         string `json:"size"`
	StorageClass string `json:"storageClass"`
	Image        string `json:"image"`
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// normalize fills defaults and rejects anything Kubernetes would refuse later,
// so a bad request fails before any object exists.
func (r *Request) normalize() (template, error) {
	r.Template = strings.ToLower(strings.TrimSpace(r.Template))
	tpl, ok := templates[r.Template]
	if !ok {
		return tpl, fmt.Errorf("unknown template %q (available: %s)", r.Template, strings.Join(Templates(), ", "))
	}
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		r.Name = r.Template
	}
	if !dnsLabel.MatchString(r.Name) || len(r.Name) > 50 {
		return tpl, fmt.Errorf("name %q must be a lowercase DNS label of at most 50 characters", r.Name)
	}
	r.Namespace = strings.TrimSpace(r.Namespace)
	if r.Namespace == "" {
		r.Namespace = defaultNS
	}
	if !dnsLabel.MatchString(r.Namespace) {
		return tpl, fmt.Errorf("namespace %q is not a valid DNS label", r.Namespace)
	}
	if strings.TrimSpace(r.Size) == "" {
		r.Size = defaultSize
	}
	q, err := resource.ParseQuantity(r.Size)
	if err != nil || q.Sign() <= 0 {
		return tpl, fmt.Errorf("size %q is not a positive quantity such as 5Gi", r.Size)
	}
	if r.Image == "" {
		r.Image = tpl.image
	}
	return tpl, nil
}

func Templates() []string {
	names := make([]string, 0, len(templates))
	for n := range templates {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// objects renders the four objects of an app. The password is passed in so
// rendering stays deterministic and testable.
func objects(r Request, tpl template, storageClass, password string) (*corev1.Secret, *corev1.PersistentVolumeClaim, *appsv1.Deployment, *corev1.Service) {
	labels := map[string]string{
		"app.kubernetes.io/name":     r.Template,
		"app.kubernetes.io/instance": r.Name,
		managedByKey:                 managedByValue,
	}
	selector := map[string]string{"app.kubernetes.io/instance": r.Name, managedByKey: managedByValue}
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: r.Namespace, Labels: labels,
			Annotations: map[string]string{templateKey: r.Template}}
	}
	secretName, pvcName := r.Name+"-auth", r.Name+"-data"

	secret := &corev1.Secret{ObjectMeta: meta(secretName), Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{"password": password}}

	sc := storageClass
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: meta(pvcName),
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &sc,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(r.Size)},
			},
		},
	}

	env := append([]corev1.EnvVar{{
		Name: tpl.secretEnv,
		ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: "password"}},
	}}, tpl.extraEnv...)

	one := int32(1)
	evictAfter := failoverSeconds
	dep := &appsv1.Deployment{
		ObjectMeta: meta(r.Name),
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Tolerations: []corev1.Toleration{
						{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists,
							Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &evictAfter},
						{Key: "node.kubernetes.io/unreachable", Operator: corev1.TolerationOpExists,
							Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &evictAfter},
					},
					Containers: []corev1.Container{{
						Name:  r.Template,
						Image: r.Image,
						Env:   env,
						Ports: []corev1.ContainerPort{{Name: "db", ContainerPort: tpl.port}},
						VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: tpl.dataDir,
							// A fresh ext4 volume carries lost+found, which both
							// images refuse as a data directory.
							SubPath: subPathFor(tpl)}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: tpl.probe}},
							InitialDelaySeconds: 5, PeriodSeconds: 5,
						},
					}},
					Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName}}}},
				},
			},
		},
	}

	svc := &corev1.Service{
		ObjectMeta: meta(r.Name),
		Spec: corev1.ServiceSpec{
			Selector: selector,
			Ports: []corev1.ServicePort{{Name: "db", Port: tpl.port,
				TargetPort: intstr.FromString("db")}},
		},
	}
	return secret, pvc, dep, svc
}

// subPathFor mounts MySQL one directory into the volume. Postgres does the
// same through PGDATA, so it mounts the volume root.
func subPathFor(tpl template) string {
	if tpl.secretEnv == "MYSQL_ROOT_PASSWORD" {
		return "mysql"
	}
	return ""
}

func randomPassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Manager creates and reports apps on one Kubernetes cluster.
type Manager struct {
	kube kubernetes.Interface
}

// NewManagerFor wraps an existing client; tests pass a fake clientset.
func NewManagerFor(kube kubernetes.Interface) *Manager { return &Manager{kube: kube} }

// NewManager connects to Kubernetes with the kubeconfig at path, or from
// inside a pod when path is empty. It returns nil, nil when neither is
// available, so a caller can run without the app tools.
func NewManager(path string) (*Manager, error) {
	var cfg *rest.Config
	var err error
	if path != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", path)
	} else if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		cfg, err = rest.InClusterConfig()
	} else {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("kubernetes config: %w", err)
	}
	cfg.Timeout = 20 * time.Second
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Manager{kube: kube}, nil
}

// storageClassFor picks the StorageClass: the one asked for, which must be
// served by the SDS driver, or else an SDS class that keeps data on the node
// running the database — a class allowing remote access would let the pod land
// on a node whose every read and write crosses the network.
func (m *Manager) storageClassFor(ctx context.Context, want string) (string, error) {
	list, err := m.kube.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("list storage classes: %w", err)
	}
	var local, remote []string
	for _, sc := range list.Items {
		if sc.Provisioner != sdsDriverName {
			if sc.Name == want {
				return "", fmt.Errorf("storage class %q is served by %s, not SDS", want, sc.Provisioner)
			}
			continue
		}
		if sc.Name == want {
			return want, nil
		}
		if sc.Parameters["allowRemoteVolumeAccess"] == "true" {
			remote = append(remote, sc.Name)
		} else {
			local = append(local, sc.Name)
		}
	}
	if want != "" {
		return "", fmt.Errorf("storage class %q not found", want)
	}
	sort.Strings(local)
	sort.Strings(remote)
	if len(local) > 0 {
		return local[0], nil
	}
	if len(remote) > 0 {
		return remote[0], nil
	}
	return "", errors.New("no StorageClass uses the SDS CSI driver; install deploy/k8s first")
}

// Created is what POST /ai/apps returns.
type Created struct {
	Message      string `json:"message"`
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	Service      string `json:"service"`
	Secret       string `json:"secret"`
	StorageClass string `json:"storageClass"`
}

// create makes the app's objects. It never overwrites: an object that already
// exists is an error, because silently adopting someone's Deployment or
// replacing a Secret would lose a password or a workload.
func (m *Manager) Create(ctx context.Context, r Request) (*Created, error) {
	tpl, err := r.normalize()
	if err != nil {
		return nil, &InvalidError{err}
	}
	sc, err := m.storageClassFor(ctx, r.StorageClass)
	if err != nil {
		return nil, &InvalidError{err}
	}
	if _, err := m.kube.AppsV1().Deployments(r.Namespace).Get(ctx, r.Name, metav1.GetOptions{}); err == nil {
		return nil, &InvalidError{fmt.Errorf("a Deployment %s/%s already exists", r.Namespace, r.Name)}
	}
	if _, err := m.kube.CoreV1().Namespaces().Get(ctx, r.Namespace, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: r.Namespace,
			Labels: map[string]string{managedByKey: managedByValue}}}
		if _, err := m.kube.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("create namespace: %w", err)
		}
	}
	pw, err := randomPassword()
	if err != nil {
		return nil, err
	}
	secret, pvc, dep, svc := objects(r, tpl, sc, pw)

	if _, err := m.kube.CoreV1().Secrets(r.Namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create secret: %w", err)
	}
	if _, err := m.kube.CoreV1().PersistentVolumeClaims(r.Namespace).Create(ctx, pvc, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create volume claim: %w", err)
	}
	if _, err := m.kube.CoreV1().Services(r.Namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create service: %w", err)
	}
	if _, err := m.kube.AppsV1().Deployments(r.Namespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create deployment: %w", err)
	}
	host := fmt.Sprintf("%s.%s.svc:%d", r.Name, r.Namespace, tpl.port)
	return &Created{
		Message: fmt.Sprintf("%s %s/%s created on %s (%s); connect at %s, password in secret %s",
			r.Template, r.Namespace, r.Name, sc, r.Size, host, secret.Name),
		Namespace: r.Namespace, Name: r.Name, Service: host, Secret: secret.Name, StorageClass: sc,
	}, nil
}

// Status is one row of GET /ai/apps.
type Status struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Template  string `json:"template"`
	Ready     bool   `json:"ready"`
	Node      string `json:"node,omitempty"`
	Phase     string `json:"phase,omitempty"`
	Claim     string `json:"claim"`
	Volume    string `json:"volume,omitempty"` // the DRBD resource behind the claim
	Service   string `json:"service"`
}

func (m *Manager) List(ctx context.Context) ([]Status, error) {
	sel := metav1.ListOptions{LabelSelector: managedByKey + "=" + managedByValue}
	deps, err := m.kube.AppsV1().Deployments("").List(ctx, sel)
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(deps.Items))
	for _, d := range deps.Items {
		st := Status{Namespace: d.Namespace, Name: d.Name, Template: d.Annotations[templateKey],
			Ready: d.Status.ReadyReplicas > 0, Claim: d.Name + "-data"}
		if tpl, ok := templates[st.Template]; ok {
			st.Service = fmt.Sprintf("%s.%s.svc:%d", d.Name, d.Namespace, tpl.port)
		}
		pods, err := m.kube.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/instance=" + d.Name + "," + managedByKey + "=" + managedByValue})
		if err == nil {
			for _, p := range pods.Items {
				if p.DeletionTimestamp != nil {
					continue // the pod left behind on a failed node
				}
				st.Node, st.Phase = p.Spec.NodeName, string(p.Status.Phase)
			}
		}
		if pvc, err := m.kube.CoreV1().PersistentVolumeClaims(d.Namespace).Get(ctx, st.Claim, metav1.GetOptions{}); err == nil && pvc.Spec.VolumeName != "" {
			if pv, err := m.kube.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{}); err == nil && pv.Spec.CSI != nil {
				st.Volume = pv.Spec.CSI.VolumeHandle
			}
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Namespace+"/"+out[i].Name < out[j].Namespace+"/"+out[j].Name
	})
	return out, nil
}

// InvalidError is a request that failed validation before anything was
// created: an unknown template, a bad name, a StorageClass that is not SDS, an
// app that already exists.
type InvalidError struct{ err error }

func (e *InvalidError) Error() string { return e.err.Error() }
func (e *InvalidError) Unwrap() error { return e.err }
