package controller

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	haifypb "github.com/haify-project/haify/api/proto/v1"
)

// Label keys are prefix/name pairs such as haify.libvirt/domain; values are
// names. Both are shown as k=v lists joined with commas, so neither may hold
// a comma, an equals sign or whitespace.
var (
	labelKeyRe   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._/-]{0,126}[A-Za-z0-9])?$`)
	labelValueRe = regexp.MustCompile(`^[A-Za-z0-9._:@+/-]{0,253}$`)
)

// SetResourceLabels sets, then removes, labels on a resource. A label given an
// empty value is removed, as `node label key=` removes one from a node.
func (s *Server) SetResourceLabels(ctx context.Context, req *haifypb.SetResourceLabelsRequest) (*haifypb.SetResourceLabelsResponse, error) {
	if s.ctrl.db == nil {
		return nil, status.Error(codes.Unavailable, "database not available")
	}
	for k, v := range req.GetLabels() {
		if !labelKeyRe.MatchString(k) {
			return nil, status.Errorf(codes.InvalidArgument, "label key %q: letters, digits and . _ / - only", k)
		}
		if !labelValueRe.MatchString(v) {
			return nil, status.Errorf(codes.InvalidArgument, "label %s: value %q may hold letters, digits and . _ : @ + / - only", k, v)
		}
	}
	res, err := s.ctrl.db.GetResource(ctx, req.GetResource())
	if err != nil || res == nil {
		return nil, status.Errorf(codes.NotFound, "resource %q not found", req.GetResource())
	}
	labels := make(map[string]string, len(res.Labels)+len(req.GetLabels()))
	for k, v := range res.Labels {
		labels[k] = v
	}
	for k, v := range req.GetLabels() {
		if v == "" {
			delete(labels, k)
			continue
		}
		labels[k] = v
	}
	for _, k := range req.GetRemove() {
		delete(labels, k)
	}
	res.Labels = labels
	if err := s.ctrl.db.SaveResource(ctx, res); err != nil {
		return nil, status.Errorf(codes.Internal, "save labels of %s: %v", res.Name, err)
	}
	return &haifypb.SetResourceLabelsResponse{
		Success: true,
		Message: fmt.Sprintf("labels of %s: %s", res.Name, labelList(labels)),
		Labels:  labels,
	}, nil
}

// labelList renders labels as sorted k=v pairs.
func labelList(labels map[string]string) string {
	if len(labels) == 0 {
		return "(none)"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ","
		}
		out += k + "=" + labels[k]
	}
	return out
}
