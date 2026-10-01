package controller

import (
	"context"
	"fmt"
	"strings"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/database"
)

func profileToProto(profile *database.ResourceProfile) *sdspb.ResourceProfile {
	if profile == nil {
		return nil
	}
	return &sdspb.ResourceProfile{
		Name:                profile.Name,
		Protocol:            profile.Protocol,
		StorageType:         profile.StorageType,
		Pool:                profile.Pool,
		Replicas:            uint32(profile.Replicas),
		ReplicasOnDifferent: append([]string(nil), profile.OnDifferent...),
		ReplicasOnSame:      append([]string(nil), profile.OnSame...),
		DrbdOptions:         cloneStringMap(profile.DRBDOptions),
		Labels:              cloneStringMap(profile.Labels),
	}
}

func profileFromProto(profile *sdspb.ResourceProfile) *database.ResourceProfile {
	if profile == nil {
		return nil
	}
	return &database.ResourceProfile{
		Name:        strings.TrimSpace(profile.Name),
		Protocol:    profile.Protocol,
		StorageType: profile.StorageType,
		Pool:        profile.Pool,
		Replicas:    int(profile.Replicas),
		OnDifferent: append([]string(nil), profile.ReplicasOnDifferent...),
		OnSame:      append([]string(nil), profile.ReplicasOnSame...),
		DRBDOptions: cloneStringMap(profile.DrbdOptions),
		Labels:      cloneStringMap(profile.Labels),
	}
}

func (s *Server) CreateResourceProfile(ctx context.Context, req *sdspb.CreateResourceProfileRequest) (*sdspb.CreateResourceProfileResponse, error) {
	if s.ctrl == nil || s.ctrl.db == nil {
		return &sdspb.CreateResourceProfileResponse{Success: false, Message: "database not available"}, nil
	}
	profile := profileFromProto(req.Profile)
	if profile == nil || profile.Name == "" {
		return &sdspb.CreateResourceProfileResponse{Success: false, Message: "profile name is required"}, nil
	}
	if err := s.ctrl.db.SaveResourceProfile(ctx, profile); err != nil {
		return &sdspb.CreateResourceProfileResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.CreateResourceProfileResponse{Success: true, Message: "Resource profile saved", Profile: profileToProto(profile)}, nil
}

func (s *Server) GetResourceProfile(ctx context.Context, req *sdspb.GetResourceProfileRequest) (*sdspb.GetResourceProfileResponse, error) {
	if s.ctrl == nil || s.ctrl.db == nil {
		return &sdspb.GetResourceProfileResponse{Success: false, Message: "database not available"}, nil
	}
	profile, err := s.ctrl.db.GetResourceProfile(ctx, req.Name)
	if err != nil {
		return &sdspb.GetResourceProfileResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.GetResourceProfileResponse{Success: true, Message: "Resource profile found", Profile: profileToProto(profile)}, nil
}

func (s *Server) ListResourceProfiles(ctx context.Context, _ *sdspb.ListResourceProfilesRequest) (*sdspb.ListResourceProfilesResponse, error) {
	if s.ctrl == nil || s.ctrl.db == nil {
		return &sdspb.ListResourceProfilesResponse{Success: false, Message: "database not available"}, nil
	}
	profiles, err := s.ctrl.db.ListResourceProfiles(ctx)
	if err != nil {
		return &sdspb.ListResourceProfilesResponse{Success: false, Message: err.Error()}, nil
	}
	result := make([]*sdspb.ResourceProfile, 0, len(profiles))
	for _, profile := range profiles {
		result = append(result, profileToProto(profile))
	}
	return &sdspb.ListResourceProfilesResponse{Success: true, Message: "Resource profiles listed", Profiles: result}, nil
}

func (s *Server) DeleteResourceProfile(ctx context.Context, req *sdspb.DeleteResourceProfileRequest) (*sdspb.DeleteResourceProfileResponse, error) {
	if s.ctrl == nil || s.ctrl.db == nil {
		return &sdspb.DeleteResourceProfileResponse{Success: false, Message: "database not available"}, nil
	}
	// A profile with members is their group: deleting it would leave each
	// pointing at nothing, and the next adjust or option change would miss
	// them without a word.
	if members, err := s.resources.profileMembers(ctx, req.Name); err == nil && len(members) > 0 {
		names := make([]string, len(members))
		for i, m := range members {
			names[i] = m.Name
		}
		return &sdspb.DeleteResourceProfileResponse{Success: false, Message: fmt.Sprintf(
			"profile %s still has %d member(s): %s; take them out first (resource set-profile <resource> --none)",
			req.Name, len(names), strings.Join(names, ", "))}, nil
	}
	if err := s.ctrl.db.DeleteResourceProfile(ctx, req.Name); err != nil {
		return &sdspb.DeleteResourceProfileResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.DeleteResourceProfileResponse{Success: true, Message: "Resource profile deleted"}, nil
}

func memberResultsToProto(results []ProfileMemberResult) ([]*sdspb.ProfileMemberResult, bool) {
	out := make([]*sdspb.ProfileMemberResult, 0, len(results))
	ok := true
	for _, r := range results {
		out = append(out, &sdspb.ProfileMemberResult{Resource: r.Resource, Success: r.OK, Message: r.Message})
		ok = ok && r.OK
	}
	return out, ok
}

func (s *Server) SetResourceProfileOptions(ctx context.Context, req *sdspb.SetResourceProfileOptionsRequest) (*sdspb.SetResourceProfileOptionsResponse, error) {
	profile, results, err := s.resources.SetProfileOptions(ctx, req.Name, req.Options)
	if err != nil {
		return &sdspb.SetResourceProfileOptionsResponse{Success: false, Message: err.Error(), Profile: profileToProto(profile)}, nil
	}
	members, ok := memberResultsToProto(results)
	msg := fmt.Sprintf("options saved on %s and applied to %d member(s)", req.Name, len(members))
	if !ok {
		msg = fmt.Sprintf("options saved on %s; some members failed", req.Name)
	}
	return &sdspb.SetResourceProfileOptionsResponse{Success: ok, Message: msg, Profile: profileToProto(profile), Members: members}, nil
}

func (s *Server) AdjustResourceProfile(ctx context.Context, req *sdspb.AdjustResourceProfileRequest) (*sdspb.AdjustResourceProfileResponse, error) {
	results, err := s.resources.AdjustProfile(ctx, req.Name, req.DryRun)
	if err != nil {
		return &sdspb.AdjustResourceProfileResponse{Success: false, Message: err.Error()}, nil
	}
	members, ok := memberResultsToProto(results)
	msg := fmt.Sprintf("%d member(s) adjusted", len(members))
	if req.DryRun {
		msg = fmt.Sprintf("dry run over %d member(s); nothing changed", len(members))
	}
	return &sdspb.AdjustResourceProfileResponse{Success: ok, Message: msg, Members: members}, nil
}

func (s *Server) GetResourceProfileMaxSize(ctx context.Context, req *sdspb.GetResourceProfileMaxSizeRequest) (*sdspb.GetResourceProfileMaxSizeResponse, error) {
	size, nodes, thin, err := s.resources.ProfileMaxSize(ctx, req.Name)
	if err != nil {
		return &sdspb.GetResourceProfileMaxSizeResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.GetResourceProfileMaxSizeResponse{Success: true, MaxSizeGb: size, Nodes: nodes, Thin: thin}, nil
}

func (s *Server) SetResourceProfile(ctx context.Context, req *sdspb.SetResourceProfileRequest) (*sdspb.SetResourceProfileResponse, error) {
	if err := s.resources.AssignProfile(ctx, req.Resource, req.Profile); err != nil {
		return &sdspb.SetResourceProfileResponse{Success: false, Message: err.Error()}, nil
	}
	if req.Profile == "" {
		return &sdspb.SetResourceProfileResponse{Success: true, Message: req.Resource + " is in no profile"}, nil
	}
	return &sdspb.SetResourceProfileResponse{Success: true, Message: fmt.Sprintf("%s is a member of %s", req.Resource, req.Profile)}, nil
}
