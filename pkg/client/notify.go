package client

import (
	"context"
	"fmt"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// NotifyChannelSpec is one alert delivery channel as a caller supplies it.
//
// Secret is write-only end to end: it is never returned by ListNotifyChannels,
// so an edit that leaves it empty keeps whatever is stored. ClearSecret is the
// only way to remove one.
type NotifyChannelSpec struct {
	Name        string
	Kind        string
	URL         string
	MinSeverity string
	Types       []string
	Headers     map[string]string
	Enabled     bool
	Secret      string
	ClearSecret bool
}

// ListNotifyChannels returns every channel (without secrets) and the message
// formats this controller can render.
func (c *SDSClient) ListNotifyChannels(ctx context.Context) ([]*sdspb.NotifyChannelInfo, []string, error) {
	resp, err := c.client.ListNotifyChannels(ctx, &sdspb.ListNotifyChannelsRequest{})
	if err != nil {
		return nil, nil, err
	}
	if !resp.Success {
		return nil, resp.Kinds, fmt.Errorf("%s", resp.Message)
	}
	return resp.Channels, resp.Kinds, nil
}

// SaveNotifyChannel creates a channel or replaces one with the same name.
func (c *SDSClient) SaveNotifyChannel(ctx context.Context, spec NotifyChannelSpec) (*sdspb.NotifyChannelInfo, error) {
	resp, err := c.client.SaveNotifyChannel(ctx, &sdspb.SaveNotifyChannelRequest{
		Name:        spec.Name,
		Kind:        spec.Kind,
		Url:         spec.URL,
		MinSeverity: spec.MinSeverity,
		Types:       spec.Types,
		Headers:     spec.Headers,
		Enabled:     spec.Enabled,
		Secret:      spec.Secret,
		ClearSecret: spec.ClearSecret,
	})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	return resp.Channel, nil
}

// DeleteNotifyChannel removes a channel.
func (c *SDSClient) DeleteNotifyChannel(ctx context.Context, name string) error {
	resp, err := c.client.DeleteNotifyChannel(ctx, &sdspb.DeleteNotifyChannelRequest{Name: name})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// TestNotifyChannel sends one message to a single channel and reports what the
// far end said. The error carries the service's own rejection text, which is
// the part that says what to fix.
func (c *SDSClient) TestNotifyChannel(ctx context.Context, name string) (string, error) {
	resp, err := c.client.TestNotifyChannel(ctx, &sdspb.TestNotifyChannelRequest{Name: name})
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("%s", resp.Message)
	}
	return resp.Message, nil
}
