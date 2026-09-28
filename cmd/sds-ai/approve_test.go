package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liliang-cn/opspilot"
)

func TestApproveNeedsAnID(t *testing.T) {
	h := approveHandler(&opspilot.Agent{})
	for _, tc := range []struct {
		method, body string
		want         int
	}{
		{http.MethodGet, "", http.StatusMethodNotAllowed},
		{http.MethodPost, `{"approve":true}`, http.StatusBadRequest},
		{http.MethodPost, `not json`, http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(tc.method, "/ai/chat/approve", strings.NewReader(tc.body)))
		if rec.Code != tc.want {
			t.Errorf("%s %q: status %d, want %d", tc.method, tc.body, rec.Code, tc.want)
		}
	}
}
