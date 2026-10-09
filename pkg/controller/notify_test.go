package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/event"
)

func TestWebhookConfigCarriesTheKindAndFilter(t *testing.T) {
	cfg, err := webhookConfigFor(&database.NotifyChannel{
		Name: "oncall", Kind: "feishu", URL: "https://open.feishu.cn/open-apis/bot/v2/hook/x",
		MinSeverity: "warning", Types: []string{"resource.failover", ""},
		Secret: "SEC", Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, event.KindFeishu, cfg.Kind)
	assert.Equal(t, event.SeverityWarning, cfg.Filter.MinSeverity)
	assert.Equal(t, []event.Type{"resource.failover"}, cfg.Filter.Types,
		"a blank entry must not become a type that matches nothing")
	assert.Equal(t, "SEC", cfg.Secret)
}

// A kind typo has to fail here, at save time, rather than at delivery: every
// message would be refused by the far end, and for Feishu, WeCom and DingTalk
// that refusal arrives inside an HTTP 200.
func TestUnknownKindIsRejected(t *testing.T) {
	_, err := webhookConfigFor(&database.NotifyChannel{
		Name: "x", Kind: "feshu", URL: "https://example.invalid/hook",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown receiver kind")
}

// A URL pasted without its scheme parses as a relative reference and fails on
// every delivery with a message about the URL rather than about the mistake.
func TestChannelURLValidation(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want string
	}{
		{"", "needs a URL"},
		{"open.feishu.cn/hook", "must start with http"},
		{"ftp://example.invalid/x", "must start with http"},
		{"https:///nohost", "no host"},
	} {
		err := validateChannelURL(tc.url)
		require.Error(t, err, "url %q", tc.url)
		assert.Contains(t, err.Error(), tc.want, "url %q", tc.url)
	}
	require.NoError(t, validateChannelURL("https://hooks.slack.com/services/T/B/x"))
	require.NoError(t, validateChannelURL("http://127.0.0.1:3076/hook"))
}

// Empty severity must mean "everything", not "nothing". ParseSeverity already
// guarantees it; this pins the wiring, because getting it backwards here would
// silently drop every alert on a channel that looks correctly configured.
func TestBlankSeverityDeliversEverything(t *testing.T) {
	cfg, err := webhookConfigFor(&database.NotifyChannel{
		Name: "all", Kind: "generic", URL: "http://127.0.0.1:3076/hook",
	})
	require.NoError(t, err)
	assert.True(t, event.SeverityInfo.AtLeast(cfg.Filter.MinSeverity))
	assert.True(t, event.SeverityCritical.AtLeast(cfg.Filter.MinSeverity))
}

// A bot URL with the wrong kind is refused at save time. The delivery path
// cannot catch this one: the raw event JSON reaches Feishu, Feishu refuses it,
// and Feishu answers HTTP 200 — success as far as anything downstream can see.
func TestWellKnownURLWithTheWrongKindIsRejected(t *testing.T) {
	for _, tc := range []struct{ kind, url, want string }{
		{"generic", "https://open.feishu.cn/open-apis/bot/v2/hook/x", "Set the kind to feishu"},
		{"feishu", "https://hooks.slack.com/services/T/B/x", "Set the kind to slack"},
		{"slack", "https://oapi.dingtalk.com/robot/send?access_token=x", "Set the kind to dingtalk"},
	} {
		_, err := webhookConfigFor(&database.NotifyChannel{Name: "x", Kind: tc.kind, URL: tc.url})
		require.Error(t, err, "kind %s url %s", tc.kind, tc.url)
		assert.Contains(t, err.Error(), tc.want)
	}
}

// An operator's own receiver is legitimately any shape at any address, so an
// unrecognised host must not be second-guessed.
func TestOwnReceiverIsNotSecondGuessed(t *testing.T) {
	_, err := webhookConfigFor(&database.NotifyChannel{
		Name: "mine", Kind: "generic", URL: "https://alerts.internal.example/hook",
	})
	require.NoError(t, err)
}
