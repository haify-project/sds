package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/event"
)

// ==================== NOTIFICATION CHANNELS ====================
//
// Where alerts go. Two rules shape this surface, both inherited from the backup
// target RPCs for the same reasons: a channel's signing secret is write-only,
// and a change takes effect immediately rather than at the next restart.

func (s *Server) ListNotifyChannels(ctx context.Context, _ *sdspb.ListNotifyChannelsRequest) (*sdspb.ListNotifyChannelsResponse, error) {
	kinds := make([]string, 0, len(event.Kinds()))
	for _, k := range event.Kinds() {
		kinds = append(kinds, string(k))
	}
	if s.ctrl.db == nil {
		return &sdspb.ListNotifyChannelsResponse{
			Success: false,
			Message: "notification channels require the controller database",
			Kinds:   kinds,
		}, nil
	}
	channels, err := s.ctrl.db.ListNotifyChannels(ctx)
	if err != nil {
		return &sdspb.ListNotifyChannelsResponse{Success: false, Message: err.Error(), Kinds: kinds}, nil
	}
	out := make([]*sdspb.NotifyChannelInfo, 0, len(channels))
	for _, c := range channels {
		out = append(out, notifyChannelInfo(c))
	}
	return &sdspb.ListNotifyChannelsResponse{
		Success: true, Message: "Notification channels listed successfully",
		Channels: out, Kinds: kinds,
	}, nil
}

func (s *Server) SaveNotifyChannel(ctx context.Context, req *sdspb.SaveNotifyChannelRequest) (*sdspb.SaveNotifyChannelResponse, error) {
	if s.ctrl.db == nil {
		return &sdspb.SaveNotifyChannelResponse{
			Success: false, Message: "notification channels require the controller database",
		}, nil
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return &sdspb.SaveNotifyChannelResponse{Success: false, Message: "a channel name is required"}, nil
	}

	ch := &database.NotifyChannel{
		Name:        name,
		Kind:        strings.TrimSpace(req.Kind),
		URL:         strings.TrimSpace(req.Url),
		MinSeverity: strings.TrimSpace(req.MinSeverity),
		Types:       req.Types,
		Headers:     req.Headers,
		Enabled:     req.Enabled,
		Secret:      req.Secret,
	}

	// An empty secret means "leave what is stored alone". A UI cannot render a
	// write-only secret in order to send it back, so without this, editing the
	// severity threshold of a signed DingTalk channel would silently unsign it
	// and every later delivery would be rejected. clear_secret is the explicit
	// way to remove one.
	if existing, err := s.ctrl.db.GetNotifyChannel(ctx, name); err == nil {
		ch.CreatedAt = existing.CreatedAt
		switch {
		case req.ClearSecret:
			ch.Secret = ""
		case req.Secret == "":
			ch.Secret = existing.Secret
		}
	}

	// Validated before it is stored, not at delivery time: a channel that
	// cannot work is a channel that silently is not alerting, and the moment to
	// find that out is while someone is looking at the form.
	if _, err := webhookConfigFor(ch); err != nil {
		return &sdspb.SaveNotifyChannelResponse{Success: false, Message: err.Error()}, nil
	}

	if err := s.ctrl.db.SaveNotifyChannel(ctx, ch); err != nil {
		return &sdspb.SaveNotifyChannelResponse{Success: false, Message: err.Error()}, nil
	}
	if err := s.ctrl.reloadNotifyChannels(ctx); err != nil {
		return &sdspb.SaveNotifyChannelResponse{
			Success: false,
			Message: fmt.Sprintf("channel %q was saved but could not be activated: %v", name, err),
			Channel: notifyChannelInfo(ch),
		}, nil
	}
	return &sdspb.SaveNotifyChannelResponse{
		Success: true,
		Message: fmt.Sprintf("Notification channel %q saved", name),
		Channel: notifyChannelInfo(ch),
	}, nil
}

func (s *Server) DeleteNotifyChannel(ctx context.Context, req *sdspb.DeleteNotifyChannelRequest) (*sdspb.DeleteNotifyChannelResponse, error) {
	if s.ctrl.db == nil {
		return &sdspb.DeleteNotifyChannelResponse{
			Success: false, Message: "notification channels require the controller database",
		}, nil
	}
	if _, err := s.ctrl.db.GetNotifyChannel(ctx, req.Name); err != nil {
		return &sdspb.DeleteNotifyChannelResponse{Success: false, Message: err.Error()}, nil
	}
	if err := s.ctrl.db.DeleteNotifyChannel(ctx, req.Name); err != nil {
		return &sdspb.DeleteNotifyChannelResponse{Success: false, Message: err.Error()}, nil
	}
	if err := s.ctrl.reloadNotifyChannels(ctx); err != nil {
		return &sdspb.DeleteNotifyChannelResponse{
			Success: false,
			Message: fmt.Sprintf("channel %q was deleted but delivery could not be reloaded: %v", req.Name, err),
		}, nil
	}
	return &sdspb.DeleteNotifyChannelResponse{
		Success: true, Message: fmt.Sprintf("Notification channel %q deleted", req.Name),
	}, nil
}

func (s *Server) TestNotifyChannel(ctx context.Context, req *sdspb.TestNotifyChannelRequest) (*sdspb.TestNotifyChannelResponse, error) {
	if s.ctrl.notify == nil {
		return &sdspb.TestNotifyChannelResponse{
			Success: false,
			Message: "notifications are disabled on this controller ([alert] enabled = false)",
		}, nil
	}
	// Bounded independently of the caller: a chat service that accepts the
	// connection and then never answers would otherwise hold the RPC open for
	// as long as the client is willing to wait.
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if err := s.ctrl.notify.Test(ctx, req.Name); err != nil {
		return &sdspb.TestNotifyChannelResponse{Success: false, Message: err.Error()}, nil
	}
	return &sdspb.TestNotifyChannelResponse{
		Success: true,
		Message: fmt.Sprintf("Test message accepted by %q", req.Name),
	}, nil
}

// notifyChannelInfo renders a stored channel for the API, without its secret.
func notifyChannelInfo(c *database.NotifyChannel) *sdspb.NotifyChannelInfo {
	info := &sdspb.NotifyChannelInfo{
		Name: c.Name, Kind: c.Kind, Url: c.URL,
		MinSeverity: c.MinSeverity, Types: c.Types, Headers: c.Headers,
		Enabled: c.Enabled, HasSecret: c.Secret != "",
	}
	if !c.CreatedAt.IsZero() {
		info.CreatedAt = c.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !c.UpdatedAt.IsZero() {
		info.UpdatedAt = c.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return info
}

// reloadNotifyChannels re-subscribes delivery after a channel changed.
//
// A controller with notifications disabled has no manager and no bus, so there
// is nothing to reload — and saving a channel there is still allowed, so the
// configuration is ready for whenever [alert] is turned on.
func (c *Controller) reloadNotifyChannels(ctx context.Context) error {
	if c.notify == nil {
		return nil
	}
	return c.notify.Reload(ctx)
}
