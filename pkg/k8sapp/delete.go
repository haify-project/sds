package k8sapp

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeleteRequest names an app to delete. Without DeleteData the claim holding
// the data and the secret holding its password are kept, so creating the app
// again with the same name picks both up.
type DeleteRequest struct {
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	DeleteData bool   `json:"deleteData"`
}

// Deleted is what Delete reports.
type Deleted struct {
	Message string   `json:"message"`
	Removed []string `json:"removed"`
	Kept    []string `json:"kept,omitempty"`
}

// managedBy reports whether an object carries the label Create puts on
// everything it makes.
func managedBy(labels map[string]string) bool { return labels[managedByKey] == managedByValue }

// Delete removes an app Create made. It deletes only objects carrying Create's
// label, and refuses outright when the Deployment does not: a name that
// happens to match must not take someone else's workload or volume with it.
//
// The Deployment goes first, so its pod releases the volume before the claim
// (with DeleteData) is deleted; the Haify StorageClass's reclaim policy then
// decides what happens to the DRBD resource behind it.
func (m *Manager) Delete(ctx context.Context, r DeleteRequest) (*Deleted, error) {
	r.Namespace = strings.TrimSpace(r.Namespace)
	if r.Namespace == "" {
		r.Namespace = defaultNS
	}
	r.Name = strings.TrimSpace(r.Name)
	if !dnsLabel.MatchString(r.Name) || !dnsLabel.MatchString(r.Namespace) {
		return nil, &InvalidError{fmt.Errorf("%q in namespace %q is not a valid app name", r.Name, r.Namespace)}
	}
	dep, err := m.kube.AppsV1().Deployments(r.Namespace).Get(ctx, r.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, &InvalidError{fmt.Errorf("no app %s/%s", r.Namespace, r.Name)}
	}
	if err != nil {
		return nil, fmt.Errorf("get deployment: %w", err)
	}
	if !managedBy(dep.Labels) {
		return nil, &InvalidError{fmt.Errorf("deployment %s/%s was not created by sds_k8s_app_create; refusing to delete it",
			r.Namespace, r.Name)}
	}

	out := &Deleted{}
	del := func(kind, name string, get func() (map[string]string, error), remove func() error) error {
		labels, err := get()
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("get %s %s: %w", kind, name, err)
		}
		if !managedBy(labels) {
			out.Kept = append(out.Kept, fmt.Sprintf("%s %s (not created by Haify)", kind, name))
			return nil
		}
		if err := remove(); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete %s %s: %w", kind, name, err)
		}
		out.Removed = append(out.Removed, kind+" "+name)
		return nil
	}
	opts := metav1.DeleteOptions{}
	apps, core := m.kube.AppsV1(), m.kube.CoreV1()
	ns, name := r.Namespace, r.Name
	if err := apps.Deployments(ns).Delete(ctx, name, opts); err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("delete deployment: %w", err)
	}
	out.Removed = append(out.Removed, "deployment "+name)
	if err := del("service", name, func() (map[string]string, error) {
		s, err := core.Services(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return s.Labels, nil
	}, func() error { return core.Services(ns).Delete(ctx, name, opts) }); err != nil {
		return nil, err
	}

	claim, secret := name+"-data", name+"-auth"
	if !r.DeleteData {
		out.Kept = append(out.Kept, "claim "+claim, "secret "+secret)
		out.Message = fmt.Sprintf("app %s/%s deleted; its data (claim %s) and password (secret %s) are kept, and "+
			"creating the app again with the same name and template uses them", ns, name, claim, secret)
		return out, nil
	}
	if err := del("claim", claim, func() (map[string]string, error) {
		p, err := core.PersistentVolumeClaims(ns).Get(ctx, claim, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return p.Labels, nil
	}, func() error { return core.PersistentVolumeClaims(ns).Delete(ctx, claim, opts) }); err != nil {
		return nil, err
	}
	if err := del("secret", secret, func() (map[string]string, error) {
		s, err := core.Secrets(ns).Get(ctx, secret, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return s.Labels, nil
	}, func() error { return core.Secrets(ns).Delete(ctx, secret, opts) }); err != nil {
		return nil, err
	}
	out.Message = fmt.Sprintf("app %s/%s and its data deleted", ns, name)
	return out, nil
}

// keptObject reports whether an object Create is about to make was kept by
// an earlier Delete of the same app: it carries Create's label and was made
// for the same template. Anything else of that name is in the way.
func keptObject(kind, name, template string, labels, annotations map[string]string) (bool, error) {
	if managedBy(labels) && annotations[templateKey] == template {
		return true, nil
	}
	return false, &InvalidError{fmt.Errorf("a %s %s already exists and is not a kept %s app's; choose another name", kind, name, template)}
}
