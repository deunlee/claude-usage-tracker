//go:build separatetest

package claude

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
)

func testCredentials(t *testing.T) *Credentials {
	t.Helper()
	credentials, err := credentialsFromFileContent(map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "sk-test", "scopes": []any{"user:profile", "user:inference"}},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return credentials
}

func TestRequestKeepsNodeHeaderOrder(t *testing.T) {
	target, _ := url.Parse(usageURL)
	encoded, err := NewClient().request(target, testCredentials(t))
	if err != nil {
		t.Fatal(err)
	}
	got := string(encoded)
	want := "GET /api/oauth/usage HTTP/1.1\r\n" +
		"Accept: application/json, text/plain, */*\r\n" +
		"Content-Type: application/json\r\n" +
		"User-Agent: claude-cli/" + CodeVersion + " (external, cli)\r\n" +
		"Authorization: Bearer sk-test\r\n" +
		"anthropic-beta: oauth-2025-04-20\r\n" +
		"Accept-Encoding: gzip, compress, deflate, br\r\n" +
		"Host: api.anthropic.com\r\n" +
		"Connection: keep-alive\r\n\r\n"
	if got != want {
		t.Errorf("request:\n%q\nwant\n%q", got, want)
	}
}

func TestGetRejectsControlCharactersInToken(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	client := NewClient()
	client.URL = server.URL + "/api/oauth/usage"
	client.RootCAs = server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	credentials, err := credentialsFromFileContent(map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "sk-test\r\nX-Injected: 1"},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Get(credentials)
	if err == nil || strings.Contains(err.Error(), "sk-test") || requests.Load() != 0 {
		t.Errorf("err = %v after %d requests, want a rejection before sending that does not echo the token", err, requests.Load())
	}
}

func TestGetDecodesCompressedResponse(t *testing.T) {
	var seen http.Header
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Content-Encoding", "br")
		writer := brotli.NewWriter(w)
		writer.Write([]byte("{\n  \"five_hour\": {\"utilization\": 45, \"resets_at\": null}\n}"))
		writer.Close()
	}))
	defer server.Close()
	client := NewClient()
	client.URL = server.URL + "/api/oauth/usage"
	client.RootCAs = server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

	response, err := client.Get(testCredentials(t))
	if err != nil {
		t.Fatal(err)
	}
	if seen.Get("Authorization") != "Bearer sk-test" || seen.Get("Anthropic-Beta") != "oauth-2025-04-20" {
		t.Errorf("server saw headers %v", seen)
	}
	if response.Status != 200 || string(response.Raw) != `{"five_hour":{"utilization":45,"resets_at":null}}` {
		t.Errorf("response %d %s", response.Status, response.Raw)
	}
	if outcome := Classify(response); outcome.Kind != OutcomeOK || outcome.Snapshot.Summary() != "5H: 45%" {
		t.Errorf("classify: %+v", outcome)
	}
}

func TestGetTimesOut(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer server.Close()
	defer close(release)
	client := NewClient()
	client.URL = server.URL
	client.timeout = 200 * time.Millisecond
	client.RootCAs = server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

	_, err := client.Get(testCredentials(t))
	if err == nil || err.Error() != "200ms 안에 응답이 없습니다" {
		t.Errorf("err = %v", err)
	}
}

func TestDecodeBody(t *testing.T) {
	text := []byte(`{"ok":true}`)
	var gz, zl, br bytes.Buffer
	gw := gzip.NewWriter(&gz)
	gw.Write(text)
	gw.Close()
	zw := zlib.NewWriter(&zl)
	zw.Write(text)
	zw.Close()
	bw := brotli.NewWriter(&br)
	bw.Write(text)
	bw.Close()
	for _, c := range []struct {
		encoding string
		body     []byte
	}{{"", text}, {"identity", text}, {"gzip", gz.Bytes()}, {" GZIP ", gz.Bytes()}, {"deflate", zl.Bytes()}, {"deflate", gz.Bytes()}, {"br", br.Bytes()}} {
		got, err := decodeBody(c.body, c.encoding)
		if err != nil || !bytes.Equal(got, text) {
			t.Errorf("decodeBody(%q) = %q, %v", c.encoding, got, err)
		}
	}
	if _, err := decodeBody(text, "zstd"); err == nil || !strings.Contains(err.Error(), "zstd") {
		t.Errorf("unsupported encoding: %v", err)
	}
}

func response(status int, body string, header http.Header) *Response {
	value, raw := parseBody([]byte(body))
	if header == nil {
		header = http.Header{}
	}
	return &Response{Status: status, Header: header, Body: value, Raw: raw}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		response *Response
		kind     string
		detail   string
	}{
		{response(200, `{"error":{"type":"rate_limit_error"}}`, nil), OutcomeRateLimited, "HTTP 200 rate_limit_error (본문 에러)"},
		{response(200, `{}`, nil), OutcomeError, "HTTP 200 응답에 사용량 필드가 없습니다"},
		{response(200, `not json`, nil), OutcomeError, "HTTP 200 응답에 사용량 필드가 없습니다"},
		{response(401, ``, nil), OutcomeAuthRejected, "HTTP 401"},
		{response(403, `{"error":{"type":"permission_error"}}`, nil), OutcomeAuthRejected, "HTTP 403 permission_error"},
		{response(403, `{"error":{"type":"forbidden"}}`, nil), OutcomeRateLimited, "HTTP 403 forbidden"},
		{response(429, ``, http.Header{"Retry-After": {"30"}}), OutcomeRateLimited, "HTTP 429"},
		{response(500, `<html>`, nil), OutcomeError, "HTTP 500"},
	}
	for _, c := range cases {
		got := Classify(c.response)
		if got.Kind != c.kind || got.Detail != c.detail {
			t.Errorf("classify(%d %v) = %s %q, want %s %q", c.response.Status, c.response.Body, got.Kind, got.Detail, c.kind, c.detail)
		}
	}
	if wait := Classify(cases[6].response).Wait; wait != 30*time.Second {
		t.Errorf("429 wait = %v", wait)
	}
}

func TestRateLimitWait(t *testing.T) {
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	for raw, want := range map[string]time.Duration{
		"":                              rateLimitDefaultWait,
		"0":                             rateLimitDefaultWait,
		"-5":                            rateLimitDefaultWait,
		" 1.5 ":                         1500 * time.Millisecond,
		"99999":                         MaxWait,
		"1e308":                         MaxWait,
		"Infinity":                      rateLimitDefaultWait,
		"Sun, 04 Oct 2026 09:02:00 GMT": 2 * time.Minute,
		"garbage":                       rateLimitDefaultWait,
	} {
		if got := rateLimitWait(http.Header{"Retry-After": {raw}}, now); got != want {
			t.Errorf("rateLimitWait(%q) = %v, want %v", raw, got, want)
		}
	}
}
