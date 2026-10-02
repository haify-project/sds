package controller

import (
	"context"
	"fmt"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

func (s *Server) SetupReplicationTLS(ctx context.Context, req *sdspb.SetupReplicationTLSRequest) (*sdspb.ReplicationTLSResponse, error) {
	states, err := s.resources.SetupReplicationTLS(ctx, req.Nodes)
	if err != nil {
		return &sdspb.ReplicationTLSResponse{Success: false, Message: err.Error()}, nil
	}
	return tlsResponse(states), nil
}

func (s *Server) GetReplicationTLSStatus(ctx context.Context, req *sdspb.GetReplicationTLSStatusRequest) (*sdspb.ReplicationTLSResponse, error) {
	states, err := s.resources.ReplicationTLSStatus(ctx, req.Nodes)
	if err != nil {
		return &sdspb.ReplicationTLSResponse{Success: false, Message: err.Error()}, nil
	}
	return tlsResponse(states), nil
}

func (s *Server) SetResourceTLS(ctx context.Context, req *sdspb.SetResourceTLSRequest) (*sdspb.SetResourceTLSResponse, error) {
	n, err := s.resources.SetResourceTLS(ctx, req.Resource, req.Enabled)
	if err != nil {
		return &sdspb.SetResourceTLSResponse{Success: false, Message: err.Error(), LinksChanged: uint32(n)}, nil
	}
	state := "off"
	if req.Enabled {
		state = "on"
	}
	return &sdspb.SetResourceTLSResponse{
		Success: true, LinksChanged: uint32(n),
		Message: fmt.Sprintf("TLS is %s for every connection of %s (%d link(s) switched)", state, req.Resource, n),
	}, nil
}

func tlsResponse(states []NodeTLSState) *sdspb.ReplicationTLSResponse {
	out := &sdspb.ReplicationTLSResponse{Success: true}
	ready := 0
	for _, st := range states {
		info := &sdspb.NodeTLSInfo{Node: st.Node, Ready: st.Ready, Problem: st.Problem}
		if !st.Expires.IsZero() {
			info.Expires = st.Expires.UTC().Format("2006-01-02")
		}
		if st.Ready {
			ready++
		}
		out.Nodes = append(out.Nodes, info)
	}
	out.Message = fmt.Sprintf("%d of %d node(s) ready for encrypted replication", ready, len(states))
	return out
}
