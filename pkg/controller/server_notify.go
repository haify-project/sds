package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/event"
)

// ==================== NOTIFICATION CHANNELS ====================
//
// Where alerts go. Two rules shape this surface, both inherited from the backup
// target RPCs for the same reasons: a channel's signing secret is write-only,
// and a change takes effect immediately rather than at the next restart.

func (s *Server) ListNotifyChannels(ctx context.Context, _ *haifypb.ListNotifyChannelsRequest) (*haifypb.ListNotifyChannelsResponse, error) {
	kinds := make([]string, 0, len(event.Kinds()))
	for _, k := range event.Kinds() {
		kinds = append(kinds, string(k))
	}
	if s.ctrl.db == nil {
		return &haifypb.ListNotifyChannelsResponse{
			Success: false,
			Message: "notification channels require the controller database",
			Kinds:   kinds,
		}, nil
	}
	channels, err := s.ctrl.db.ListNotifyChannels(ctx)
	if err != nil {
		return &haifypb.ListNotifyChannelsResponse{Success: false, Message: err.Error(), Kinds: kinds}, nil
	}
	out := make([]*haifypb.NotifyChannelInfo, 0, len(channels))
	for _, c := range channels {
		out = append(out, notifyChannelInfo(c))
	}
	return &haifypb.ListNotifyChannelsResponse{
		Success: true, Message: "Notification channels listed successfully",
		Channels: out, Kinds: kinds,
	}, nil
}

func (s *Server) SaveNotifyChannel(ctx context.Context, req *haifypb.SaveNotifyChannelRequest) (*haifypb.SaveNotifyChannelResponse, error) {
	if s.ctrl.db == nil {
		return &haifypb.SaveNotifyChannelResponse{
			Success: false, Message: "notification channels require the controller database",
		}, nil
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return &haifypb.SaveNotifyChannelResponse{Success: false, Message: "a channel name is required"}, nil
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
		return &haifypb.SaveNotifyChannelResponse{Success: false, Message: err.Error()}, nil
	}

	if err := s.ctrl.db.SaveNotifyChannel(ctx, ch); err != nil {
		return &haifypb.SaveNotifyChannelResponse{Success: false, Message: err.Error()}, nil
	}
	if err := s.ctrl.reloadNotifyChannels(ctx); err != nil {
		return &haifypb.SaveNotifyChannelResponse{
			Success: false,
			Message: fmt.Sprintf("channel %q was saved but could not be activated: %v", name, err),
			Channel: notifyChannelInfo(ch),
		}, nil
	}
	return &haifypb.SaveNotifyChannelResponse{
		Success: true,
		Message: fmt.Sprintf("Notification channel %q saved", name),
		Channel: notifyChannelInfo(ch),
	}, nil
}

func (s *Server) DeleteNotifyChannel(ctx context.Context, req *haifypb.DeleteNotifyChannelRequest) (*haifypb.DeleteNotifyChannelResponse, error) {
	if s.ctrl.db == nil {
		return &haifypb.DeleteNotifyChannelResponse{
			Success: false, Message: "notification channels require the controller database",
		}, nil
	}
	if _, err := s.ctrl.db.GetNotifyChannel(ctx, req.Name); err != nil {
		return &haifypb.DeleteNotifyChannelResponse{Success: false, Message: err.Error()}, nil
	}
	if err := s.ctrl.db.DeleteNotifyChannel(ctx, req.Name); err != nil {
		return &haifypb.DeleteNotifyChannelResponse{Success: false, Message: err.Error()}, nil
	}
	if err := s.ctrl.reloadNotifyChannels(ctx); err != nil {
		return &haifypb.DeleteNotifyChannelResponse{
			Success: false,
			Message: fmt.Sprintf("channel %q was deleted but delivery could not be reloaded: %v", req.Name, err),
		}, nil
	}
	return &haifypb.DeleteNotifyChannelResponse{
		Success: true, Message: fmt.Sprintf("Notification channel %q deleted", req.Name),
	}, nil
}

func (s *Server) TestNotifyChannel(ctx context.Context, req *haifypb.TestNotifyChannelRequest) (*haifypb.TestNotifyChannelResponse, error) {
	if s.ctrl.notify == nil {
		return &haifypb.TestNotifyChannelResponse{
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
		return &haifypb.TestNotifyChannelResponse{Success: false, Message: err.Error()}, nil
	}
	return &haifypb.TestNotifyChannelResponse{
		Success: true,
		Message: fmt.Sprintf("Test message accepted by %q", req.Name),
	}, nil
}

// notifyChannelInfo renders a stored channel for the API, without its secret.
func notifyChannelInfo(c *database.NotifyChannel) *haifypb.NotifyChannelInfo {
	info := &haifypb.NotifyChannelInfo{
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
