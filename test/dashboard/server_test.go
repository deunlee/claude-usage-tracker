//go:build separatetest

package dashboard

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"claudeusage/internal/claude"
	"claudeusage/internal/poller"
	"claudeusage/internal/store"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	configDir := claude.ExplicitConfigDir(t.TempDir(), t.TempDir())
	p := poller.New(configDir, claude.NewClient(), store.NewArchive(t.TempDir(), t.TempDir()), &claude.TokenRefresher{}, nil)
	return New("127.0.0.1", 8787, Access{}, store.NewIndex(t.TempDir(), poller.BaseInterval), []*poller.Poller{p}, nil)
}

func TestAPIRequiresPassword(t *testing.T) {
	d := testServer(t)
	d.access.Password = "비밀 pw"
	request := func(method, target, authorization string) int {
		r := httptest.NewRequest(method, target, nil)
		r.Host = "127.0.0.1:8787"
		if authorization != "" {
			r.Header.Set("Authorization", authorization)
		}
		recorder := httptest.NewRecorder()
		d.ServeHTTP(recorder, r)
		return recorder.Code
	}
	right := "Bearer " + base64.StdEncoding.EncodeToString([]byte("비밀 pw"))
	wrong := "Bearer " + base64.StdEncoding.EncodeToString([]byte("비밀"))
	for _, c := range []struct {
		method, target, authorization string
		status                        int
	}{
		{"GET", "/", "", http.StatusOK},
		{"GET", "/login", "", http.StatusOK},
		{"GET", "/api/dashboard", "", http.StatusUnauthorized},
		{"GET", "/api/dashboard", wrong, http.StatusUnauthorized},
		{"GET", "/api/dashboard", "Basic " + base64.StdEncoding.EncodeToString([]byte("비밀 pw")), http.StatusUnauthorized},
		{"GET", "/api/dashboard", "Bearer not base64", http.StatusUnauthorized},
		{"GET", "/api/dashboard", right, http.StatusOK},
		{"POST", "/api/refresh", "", http.StatusUnauthorized},
		{"POST", "/api/login", "", http.StatusUnauthorized},
		{"POST", "/api/login", wrong, http.StatusUnauthorized},
		{"POST", "/api/login", right, http.StatusOK},
		{"GET", "/api/login", right, http.StatusMethodNotAllowed},
	} {
		if got := request(c.method, c.target, c.authorization); got != c.status {
			t.Errorf("%s %s with %q = %d, want %d", c.method, c.target, c.authorization, got, c.status)
		}
	}
}

func TestEmptyPasswordLogsInWithoutHeader(t *testing.T) {
	d := testServer(t)
	if got := serve(d, "POST", "127.0.0.1:8787", "/api/login"); got.Code != http.StatusOK {
		t.Errorf("POST /api/login = %d", got.Code)
	}
}

func prefixes(texts ...string) []netip.Prefix {
	list := make([]netip.Prefix, len(texts))
	for i, text := range texts {
		list[i] = netip.MustParsePrefix(text)
	}
	return list
}

func TestClientAddr(t *testing.T) {
	trusted := prefixes("127.0.0.1/32", "10.0.0.0/8")
	for _, c := range []struct {
		remote string
		xff    []string
		want   string
	}{
		{"192.0.2.1:1234", nil, "192.0.2.1"},
		// 믿지 않는 연결이 보낸 헤더는 꾸민 것일 수 있다.
		{"192.0.2.1:1234", []string{"198.51.100.7"}, "192.0.2.1"},
		{"127.0.0.1:1234", nil, "127.0.0.1"},
		{"127.0.0.1:1234", []string{"198.51.100.7"}, "198.51.100.7"},
		{"127.0.0.1:1234", []string{"203.0.113.9, 198.51.100.7"}, "198.51.100.7"},
		{"127.0.0.1:1234", []string{"203.0.113.9, 198.51.100.7, 10.1.2.3"}, "198.51.100.7"},
		{"127.0.0.1:1234", []string{"203.0.113.9", "198.51.100.7,10.1.2.3"}, "198.51.100.7"},
		{"127.0.0.1:1234", []string{"10.9.9.9, 10.1.2.3"}, "10.9.9.9"},
		{"[::ffff:127.0.0.1]:1234", []string{"::ffff:198.51.100.7"}, "198.51.100.7"},
		{"[fe80::1%eth0]:1234", nil, "fe80::1"},
		{"127.0.0.1:1234", []string{"unknown"}, ""},
		{"127.0.0.1:1234", []string{"198.51.100.7:5678"}, ""},
		{"not an address", nil, ""},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		for _, value := range c.xff {
			r.Header.Add("X-Forwarded-For", value)
		}
		got, ok := clientAddr(r, trusted)
		if c.want == "" {
			if ok {
				t.Errorf("clientAddr(%s, %q) = %s, want unknown", c.remote, c.xff, got)
			}
		} else if !ok || got.String() != c.want {
			t.Errorf("clientAddr(%s, %q) = %s, %v, want %s", c.remote, c.xff, got, ok, c.want)
		}
	}
}

func TestIPAllowListSkipsPassword(t *testing.T) {
	d := testServer(t)
	d.access = Access{Password: "pw", AllowedIPs: prefixes("192.168.0.0/24", "203.0.113.9/32"), TrustedProxies: prefixes("127.0.0.1/32")}
	right := "Bearer " + base64.StdEncoding.EncodeToString([]byte("pw"))
	wrong := "Bearer " + base64.StdEncoding.EncodeToString([]byte("틀림"))
	for _, c := range []struct {
		method, target, remote, xff, authorization string
		status                                     int
	}{
		{"GET", "/api/dashboard", "192.168.0.50:1234", "", "", http.StatusOK},
		{"GET", "/api/dashboard", "192.168.0.50:1234", "", wrong, http.StatusOK},
		{"GET", "/api/dashboard", "192.168.1.50:1234", "", "", http.StatusUnauthorized},
		{"GET", "/api/dashboard", "192.168.1.50:1234", "", right, http.StatusOK},
		{"GET", "/api/dashboard", "127.0.0.1:1234", "203.0.113.9", "", http.StatusOK},
		{"GET", "/api/dashboard", "127.0.0.1:1234", "203.0.113.9, 198.51.100.7", "", http.StatusUnauthorized},
		{"GET", "/api/dashboard", "198.51.100.7:1234", "203.0.113.9", "", http.StatusUnauthorized},
		{"POST", "/api/refresh", "192.168.0.50:1234", "", "", http.StatusOK},
		{"POST", "/api/login", "192.168.0.50:1234", "", "", http.StatusUnauthorized},
		{"POST", "/api/login", "192.168.0.50:1234", "", wrong, http.StatusUnauthorized},
		{"POST", "/api/login", "192.168.0.50:1234", "", right, http.StatusOK},
	} {
		r := httptest.NewRequest(c.method, c.target, nil)
		r.Host = "127.0.0.1:8787"
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if c.authorization != "" {
			r.Header.Set("Authorization", c.authorization)
		}
		recorder := httptest.NewRecorder()
		d.ServeHTTP(recorder, r)
		if recorder.Code != c.status {
			t.Errorf("%s %s from %s (xff %q, auth %q) = %d, want %d", c.method, c.target, c.remote, c.xff, c.authorization, recorder.Code, c.status)
		}
	}
}

func TestDashboardPayloadTellsIPAllowed(t *testing.T) {
	d := testServer(t)
	for _, c := range []struct {
		allowed []netip.Prefix
		want    bool
	}{
		{nil, false},
		{prefixes("192.0.2.0/24"), true},
		{prefixes("198.51.100.0/24"), false},
	} {
		d.access.AllowedIPs = c.allowed
		// httptest.NewRequest는 192.0.2.1에서 온 요청으로 만든다.
		got := serve(d, "GET", "127.0.0.1:8787", "/api/dashboard")
		var payload struct {
			IPAllowed *bool `json:"ipAllowed"`
		}
		if err := json.Unmarshal(got.Body.Bytes(), &payload); err != nil || payload.IPAllowed == nil || *payload.IPAllowed != c.want {
			t.Errorf("allowed %v: ipAllowed = %v, %v, want %v", c.allowed, payload.IPAllowed, err, c.want)
		}
	}
}

func serve(d *Server, method, host, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	request.Host = host
	recorder := httptest.NewRecorder()
	d.ServeHTTP(recorder, request)
	return recorder
}

func TestDashboardServerGuards(t *testing.T) {
	d := testServer(t)
	for _, c := range []struct {
		method, host, target string
		status               int
	}{
		{"GET", "evil.example:8787", "/", http.StatusMisdirectedRequest},
		{"POST", "127.0.0.1:8787", "/", http.StatusMethodNotAllowed},
		{"GET", "localhost:8787", "/../config.json", http.StatusNotFound},
		{"GET", "[::1]:8787", "/", http.StatusOK},
	} {
		if got := serve(d, c.method, c.host, c.target); got.Code != c.status {
			t.Errorf("%s %s %s = %d, want %d", c.method, c.host, c.target, got.Code, c.status)
		}
	}
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	head := serve(d, "HEAD", "127.0.0.1:8787", "/")
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(len(index)) || head.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("HEAD / = %d %q %v", head.Code, head.Body.String(), head.Header())
	}
}

// 목록에 적은 파일이 static/에 없으면 빌드는 통과하고 요청 때에야 500이 나므로 여기서 잡는다.
func TestServesEveryStaticFile(t *testing.T) {
	d := testServer(t)
	for path := range staticFiles {
		if got := serve(d, "GET", "127.0.0.1:8787", path); got.Code != http.StatusOK {
			t.Errorf("GET %s = %d %s", path, got.Code, got.Body.String())
		}
	}
}

// 스크립트가 찾는 요소의 id가 HTML에서 빠지면 렌더링이 그 자리에서 멈추고, 화면에는 데이터를 불러오지 못했다고만 나온다.
func TestScriptsFindTheirElements(t *testing.T) {
	lookup := regexp.MustCompile(`(?:\$|getElementById)\('([^']+)'\)`)
	for page, scripts := range map[string][]string{
		"index.html": {"app.js", "clawd.js"},
		"clawd.html": {"gallery.js"},
		"login.html": {"login.js"},
	} {
		html, err := staticFS.ReadFile("static/" + page)
		if err != nil {
			t.Fatal(err)
		}
		for _, script := range scripts {
			source, err := staticFS.ReadFile("static/" + script)
			if err != nil {
				t.Fatal(err)
			}
			for _, match := range lookup.FindAllSubmatch(source, -1) {
				if !strings.Contains(string(html), `id="`+string(match[1])+`"`) {
					t.Errorf("%s looks up #%s, which %s does not have", script, match[1], page)
				}
			}
		}
	}
}

func TestDashboardPayloadBeforeFirstPoll(t *testing.T) {
	got := serve(testServer(t), "GET", "127.0.0.1:8787", "/api/dashboard?range=7d&account=5")
	if got.Code != http.StatusOK {
		t.Fatalf("status %d: %s", got.Code, got.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"generatedAt", "accounts", "account", "range", "catalog", "points", "activity", "latest", "status"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("payload has no %q: %s", key, got.Body.String())
		}
	}
	latest := payload["latest"].(map[string]any)
	if payload["account"] != 0.0 || payload["range"].(map[string]any)["key"] != "7d" ||
		latest["fetchedAt"] != nil || latest["breakdown"] != nil || len(latest["readings"].([]any)) != 2 {
		t.Errorf("payload %s", got.Body.String())
	}
	status := payload["status"].(map[string]any)
	if status["email"] != nil || status["token"] != nil || status["schedule"].(map[string]any)["intervalMs"] != 120000.0 {
		t.Errorf("status %v", status)
	}
	for _, key := range []string{"configDir", "accountFile", "credentialsPath"} {
		if strings.Contains(got.Body.String(), `"`+key+`"`) {
			t.Errorf("payload exposes %s: %s", key, got.Body.String())
		}
	}
}

func TestRefreshEndpoint(t *testing.T) {
	d := testServer(t)
	request := func(method, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/refresh?account=0", nil)
		r.Host = "127.0.0.1:8787"
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		recorder := httptest.NewRecorder()
		d.ServeHTTP(recorder, r)
		return recorder
	}
	if got := request("GET", ""); got.Code != http.StatusMethodNotAllowed || got.Header().Get("Allow") != "POST" {
		t.Errorf("GET = %d %v", got.Code, got.Header())
	}
	if got := request("POST", "http://evil.example"); got.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d", got.Code)
	}
	// 시작하지 않은 수집기는 요청을 받아 가지 않으므로, 기다리지 않고 지금 상태로 답하는지 본다.
	defer func(saved time.Duration) { refreshAckTimeout = saved }(refreshAckTimeout)
	refreshAckTimeout = 10 * time.Millisecond
	for _, origin := range []string{"http://127.0.0.1:8787", ""} {
		got := request("POST", origin)
		var status map[string]any
		if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &status) != nil || status["schedule"] == nil {
			t.Errorf("POST with origin %q = %d %s", origin, got.Code, got.Body.String())
		}
	}
}

func TestRefreshRequestsEveryAccount(t *testing.T) {
	newPoller := func() *poller.Poller {
		configDir := claude.ExplicitConfigDir(t.TempDir(), t.TempDir())
		return poller.New(configDir, claude.NewClient(), store.NewArchive(t.TempDir(), t.TempDir()), &claude.TokenRefresher{}, nil)
	}
	pollers := []*poller.Poller{newPoller(), newPoller()}
	d := New("127.0.0.1", 8787, Access{}, store.NewIndex(t.TempDir(), poller.BaseInterval), pollers, nil)
	defer func(saved time.Duration) { refreshAckTimeout = saved }(refreshAckTimeout)
	refreshAckTimeout = 10 * time.Millisecond

	if got := serve(d, "POST", "127.0.0.1:8787", "/api/refresh?account=0"); got.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", got.Code, got.Body.String())
	}
	// 시작하지 않은 수집기는 요청을 한 칸 쌓아 두고, 쌓인 요청이 있으면 다음 요청은 바로 끝난 것으로 돌려준다.
	for i, p := range pollers {
		select {
		case <-p.RequestRefresh():
		default:
			t.Errorf("poller %d got no refresh request", i)
		}
	}
}

var errAcceptFailed = errors.New("accept failed")

type brokenListener struct{}

func (brokenListener) Accept() (net.Conn, error) { return nil, errAcceptFailed }
func (brokenListener) Close() error              { return nil }
func (brokenListener) Addr() net.Addr            { return &net.TCPAddr{} }

func TestServeReportsUnexpectedStop(t *testing.T) {
	if err, ok := <-testServer(t).serve(brokenListener{}); !ok || !errors.Is(err, errAcceptFailed) {
		t.Errorf("stopped with %v (received %v), want %v", err, ok, errAcceptFailed)
	}
}

func TestServeIsQuietAfterClose(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := testServer(t)
	stopped := d.serve(listener)
	d.Close()
	if err, ok := <-stopped; ok {
		t.Errorf("reported %v after Close", err)
	}
}

func TestAccountEntryUsesAliasByEmailIgnoringCase(t *testing.T) {
	d := &Server{aliases: claude.AccountAliases{"me@example.com": "개인"}}
	email, other := "Me@Example.com", "other@example.com"

	if entry := d.accountEntry(&email); entry.Alias == nil || *entry.Alias != "개인" || *entry.Email != email {
		t.Errorf("entry %+v", entry)
	}
	if entry := d.accountEntry(&other); entry.Alias != nil {
		t.Errorf("unaliased entry has alias %q", *entry.Alias)
	}
	if entry := d.accountEntry(nil); entry.Alias != nil || entry.Email != nil {
		t.Errorf("entry without email %+v", entry)
	}
}
