package event

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleEvent() Event {
	return Event{
		ID:        7,
		Type:      TypeResourceNoPrimary,
		Severity:  SeverityCritical,
		Status:    StatusFiring,
		Resource:  "pgha",
		Node:      "orange1",
		Message:   "resource pgha has no Primary: orange1 was demoted and nothing took over",
		Details:   map[string]string{"from": "orange1"},
		Timestamp: time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC),
	}
}

// Each chat service defines its own envelope and rejects anything else. These
// assert the exact keys, because a payload that is merely "reasonable JSON" is
// refused just as completely as no payload at all.
func TestRenderProducesEachServicesEnvelope(t *testing.T) {
	e := sampleEvent()

	t.Run("feishu", func(t *testing.T) {
		body, err := KindFeishu.Render(e)
		require.NoError(t, err)
		var got struct {
			MsgType string            `json:"msg_type"`
			Content map[string]string `json:"content"`
		}
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, "text", got.MsgType)
		assert.Contains(t, got.Content["text"], "has no Primary")
		assert.Contains(t, got.Content["text"], "FIRING")
	})

	t.Run("slack", func(t *testing.T) {
		body, err := KindSlack.Render(e)
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(body, &got))
		require.Contains(t, got, "text", "Slack rejects a payload without a text field")
		assert.Contains(t, got["text"], "has no Primary")
	})

	t.Run("wecom", func(t *testing.T) {
		body, err := KindWeCom.Render(e)
		require.NoError(t, err)
		var got struct {
			MsgType  string            `json:"msgtype"`
			Markdown map[string]string `json:"markdown"`
		}
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, "markdown", got.MsgType)
		assert.Contains(t, got.Markdown["content"], "has no Primary")
	})

	t.Run("dingtalk", func(t *testing.T) {
		body, err := KindDingTalk.Render(e)
		require.NoError(t, err)
		var got struct {
			MsgType  string            `json:"msgtype"`
			Markdown map[string]string `json:"markdown"`
		}
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, "markdown", got.MsgType)
		assert.NotEmpty(t, got.Markdown["title"], "DingTalk refuses a markdown message with no title")
		assert.Contains(t, got.Markdown["text"], "has no Primary")
	})

	t.Run("generic is the event itself", func(t *testing.T) {
		body, err := KindGeneric.Render(e)
		require.NoError(t, err)
		var back Event
		require.NoError(t, json.Unmarshal(body, &back))
		assert.Equal(t, e.ID, back.ID)
		assert.Equal(t, e.Type, back.Type)
		assert.Equal(t, e.Details, back.Details)
	})
}

// Every rendering must carry the message and say whether the condition started
// or ended. A channel that cannot tell a new outage from a recovery is worse
// than no channel.
func TestEveryKindCarriesTheMessageAndStatus(t *testing.T) {
	for _, k := range Kinds() {
		if k == KindGeneric {
			continue
		}
		firing, err := k.Render(sampleEvent())
		require.NoError(t, err)

		resolved := sampleEvent()
		resolved.Status = StatusResolved
		resolved.Severity = SeverityInfo
		resolved.Message = "resource pgha has a Primary again on orange1"
		cleared, err := k.Render(resolved)
		require.NoError(t, err)

		assert.Contains(t, string(firing), "FIRING", "kind %s", k)
		assert.Contains(t, string(cleared), "RESOLVED", "kind %s", k)
		assert.NotEqual(t, string(firing), string(cleared), "kind %s", k)
	}
}

// A kind typo must not fall back to a format the far end will refuse. See
// ParseKind's comment for why this differs from ParseSeverity.
func TestParseKindRejectsUnknown(t *testing.T) {
	for _, in := range []string{"feishu", "FEISHU", " slack ", "wecom", "dingtalk", "generic", ""} {
		_, err := ParseKind(in)
		assert.NoError(t, err, "input %q", in)
	}
	_, err := ParseKind("feshu")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown receiver kind")
}

// DingTalk's 加签 mode: HMAC-SHA256 over "<timestamp>\n<secret>", base64, in the
// query string alongside the timestamp it covers.
func TestDingTalkSigning(t *testing.T) {
	now := time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC)
	signed, err := KindDingTalk.SignedURL("https://oapi.dingtalk.com/robot/send?access_token=abc", "SECRET", now)
	require.NoError(t, err)

	u, err := url.Parse(signed)
	require.NoError(t, err)
	ms := strconv.FormatInt(now.UnixMilli(), 10)
	assert.Equal(t, ms, u.Query().Get("timestamp"))
	assert.Equal(t, "abc", u.Query().Get("access_token"), "the existing query must survive")

	mac := hmac.New(sha256.New, []byte("SECRET"))
	mac.Write([]byte(ms + "\n" + "SECRET"))
	assert.Equal(t, base64.StdEncoding.EncodeToString(mac.Sum(nil)), u.Query().Get("sign"))
}

func TestSigningIsSkippedWithoutASecretOrForOtherKinds(t *testing.T) {
	const raw = "https://example.invalid/hook"
	for _, tc := range []struct {
		kind   Kind
		secret string
	}{
		{KindDingTalk, ""},
		{KindFeishu, "SECRET"},
		{KindSlack, "SECRET"},
		{KindGeneric, "SECRET"},
	} {
		got, err := tc.kind.SignedURL(raw, tc.secret, time.Now())
		require.NoError(t, err)
		assert.Equal(t, raw, got, "kind %s", tc.kind)
	}
}

// The one that makes alerting silently useless: these services answer HTTP 200
// for a message they refused. A delivery path that stops at the status code
// reports every rejection as a success.
func TestRejectionInsideA200IsAnError(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind Kind
		body string
		want string
	}{
		{"feishu bad token", KindFeishu, `{"code":19021,"msg":"sign match fail"}`, "19021"},
		{"wecom bad key", KindWeCom, `{"errcode":93000,"errmsg":"invalid webhook url"}`, "93000"},
		{"dingtalk keyword", KindDingTalk, `{"errcode":310000,"errmsg":"keywords not in content"}`, "310000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.kind.CheckResponse([]byte(tc.body))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestAcceptedRepliesAreNotErrors(t *testing.T) {
	require.NoError(t, KindFeishu.CheckResponse([]byte(`{"code":0,"msg":"success"}`)))
	require.NoError(t, KindWeCom.CheckResponse([]byte(`{"errcode":0,"errmsg":"ok"}`)))
	require.NoError(t, KindDingTalk.CheckResponse([]byte(`{"errcode":0,"errmsg":"ok"}`)))
	// Slack answers a bare "ok" and uses status codes honestly; a generic
	// receiver is whatever the operator built. Neither body is ours to judge.
	require.NoError(t, KindSlack.CheckResponse([]byte("ok")))
	require.NoError(t, KindGeneric.CheckResponse([]byte("anything at all")))
}

// A non-JSON body from a service that always answers JSON is a proxy or a login
// page, not a delivery.
func TestNonJSONReplyFromAChatServiceIsAnError(t *testing.T) {
	err := KindFeishu.CheckResponse([]byte("<html>502 Bad Gateway</html>"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected reply")
}

// The most damaging mistake this feature can make is silent: a Feishu bot URL
// with the kind left on "generic" posts raw JSON, is refused, and is answered
// HTTP 200. The body cannot expose it — a generic receiver's body belongs to
// the operator — but the host can, before anything is sent.
func TestKindForURL(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want Kind
	}{
		{"https://open.feishu.cn/open-apis/bot/v2/hook/abc", KindFeishu},
		{"https://open.larksuite.com/open-apis/bot/v2/hook/abc", KindFeishu},
		{"https://hooks.slack.com/services/T00/B00/xyz", KindSlack},
		{"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc", KindWeCom},
		{"https://oapi.dingtalk.com/robot/send?access_token=abc", KindDingTalk},
	} {
		got, known := KindForURL(tc.url)
		require.True(t, known, "url %s", tc.url)
		assert.Equal(t, tc.want, got, "url %s", tc.url)
	}
}

// A host we do not recognise must not be guessed at: an operator's own receiver
// is legitimately anything, and refusing it would be worse than saying nothing.
func TestKindForURLStaysSilentOnUnknownHosts(t *testing.T) {
	for _, u := range []string{
		"https://alerts.internal.example/hook",
		"http://127.0.0.1:34877/feishu",
		"not a url at all",
		"",
	} {
		_, known := KindForURL(u)
		assert.False(t, known, "url %q", u)
	}
}

// Suffix matching must not fire on a lookalike domain.
func TestKindForURLDoesNotMatchLookalikeDomains(t *testing.T) {
	for _, u := range []string{
		"https://notfeishu.cn/hook",
		"https://feishu.cn.evil.example/hook",
		"https://myslack.com/hook",
	} {
		_, known := KindForURL(u)
		assert.False(t, known, "url %q", u)
	}
}
