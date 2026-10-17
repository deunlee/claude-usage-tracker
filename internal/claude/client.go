package claude

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
)

// 요청 형태를 바이너리에서 확인한 로컬 설치본의 버전. Claude Code를 업데이트하면 여기도 직접 맞춘다.
const CodeVersion = "2.1.302"
const usageURL = "https://api.anthropic.com/api/oauth/usage"

// Claude Code가 이 호출에 거는 타임아웃.
const requestTimeout = 5 * time.Second

// 429를 받았을 때 Claude Code와 같은 대기: retry-after만큼(0이거나 없으면 5분), 최대 1시간.
const rateLimitDefaultWait = 5 * time.Minute
const MaxWait = time.Hour

const (
	OutcomeOK           = "ok"
	OutcomeRateLimited  = "rate_limited"
	OutcomeAuthRejected = "auth_rejected"
	OutcomeError        = "error"
)

type Response struct {
	Status int
	Header http.Header
	// 본문이 JSON이면 파싱한 값, 아니면 원문 문자열이다.
	Body any
	// 본문이 JSON일 때 공백만 뺀 원문. 아카이브에는 이걸 쓴다.
	Raw      json.RawMessage
	Duration time.Duration
}

type Outcome struct {
	Kind     string
	Snapshot *Snapshot
	Wait     time.Duration
	Detail   string
}

type Client struct {
	URL       string
	timeout   time.Duration
	UserAgent string
	RootCAs   *x509.CertPool
}

func NewClient() *Client {
	return &Client{
		URL:       usageURL,
		timeout:   requestTimeout,
		UserAgent: "claude-cli/" + CodeVersion + " (external, cli)",
	}
}

// Claude Code(axios)가 보내는 헤더와 이름·값·순서를 맞춘다:
// axios 기본 헤더 → 요청 헤더(User-Agent, 인증, beta, Content-Type) → HTTP 어댑터의 Accept-Encoding.
func (c *Client) headers(credentials *Credentials) [][2]string {
	return [][2]string{
		{"Accept", "application/json, text/plain, */*"},
		{"Content-Type", "application/json"},
		{"User-Agent", c.UserAgent},
		{"Authorization", credentials.authorization()},
		{"anthropic-beta", "oauth-2025-04-20"},
		{"Accept-Encoding", "gzip, compress, deflate, br"},
	}
}

// net/http는 헤더를 이름순으로 다시 늘어놓고 HTTP/2로 올라가기도 해서, 요청은 HTTP/1.1로 직접 쓴다.
// 맨 뒤의 Host와 Connection: keep-alive는 Node 런타임이 붙이던 자리와 값 그대로다.
func (c *Client) request(target *url.URL, credentials *Credentials) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\n", target.RequestURI())
	for _, header := range c.headers(credentials) {
		// net/http를 거치지 않으니 그쪽이 하던 검사를 여기서 한다. 크리덴셜 파일의 토큰에 줄바꿈이 섞여 있으면 다른 헤더가 끼어든다.
		if !validHeaderValue(header[1]) {
			// 값에 토큰이 들어 있을 수 있어 오류 메시지에 옮기지 않는다.
			return nil, fmt.Errorf("%s 헤더 값에 쓸 수 없는 제어 문자가 있습니다", header[0])
		}
		fmt.Fprintf(&b, "%s: %s\r\n", header[0], header[1])
	}
	fmt.Fprintf(&b, "Host: %s\r\nConnection: keep-alive\r\n\r\n", target.Host)
	return b.Bytes(), nil
}

// RFC 9110의 field-value: 탭, 공백, 보이는 ASCII 문자, 0x80 이상 바이트만 받는다.
func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if c := value[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

func (c *Client) Get(credentials *Credentials) (*Response, error) {
	started := time.Now()
	target, err := url.Parse(c.URL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	response, err := c.roundTrip(ctx, target, credentials)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, fmt.Errorf("%dms 안에 응답이 없습니다", c.timeout.Milliseconds())
		}
		return nil, err
	}
	response.Duration = time.Since(started)
	return response, nil
}

func (c *Client) roundTrip(ctx context.Context, target *url.URL, credentials *Credentials) (*Response, error) {
	request, err := c.request(target, credentials)
	if err != nil {
		return nil, err
	}
	port := target.Port()
	if port == "" {
		port = "443"
	}
	dialer := &tls.Dialer{Config: &tls.Config{ServerName: target.Hostname(), RootCAs: c.RootCAs}}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(target.Hostname(), port))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	if _, err := conn.Write(request); err != nil {
		return nil, err
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	encoded, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeBody(encoded, strings.Join(res.Header.Values("Content-Encoding"), ", "))
	if err != nil {
		return nil, err
	}
	body, raw := parseBody(decoded)
	return &Response{Status: res.StatusCode, Header: res.Header, Body: body, Raw: raw}, nil
}

func Classify(response *Response) Outcome {
	errType := errorType(response.Body)
	detail := "HTTP " + strconv.Itoa(response.Status)
	if errType != "" {
		detail += " " + errType
	}
	if response.Status >= 200 && response.Status < 300 {
		if isUsageBody(response.Body) {
			return Outcome{Kind: OutcomeOK, Snapshot: &Snapshot{data: response.Body.(map[string]any), Raw: response.Raw}}
		}
		if errType == "rate_limit_error" {
			return Outcome{Kind: OutcomeRateLimited, Wait: rateLimitDefaultWait, Detail: detail + " (본문 에러)"}
		}
		return Outcome{Kind: OutcomeError, Detail: detail + " 응답에 사용량 필드가 없습니다"}
	}
	if response.Status == 401 || (response.Status == 403 && (errType == "authentication_error" || errType == "permission_error")) {
		return Outcome{Kind: OutcomeAuthRejected, Detail: detail}
	}
	if response.Status == 429 || response.Status == 403 {
		return Outcome{Kind: OutcomeRateLimited, Wait: rateLimitWait(response.Header, time.Now()), Detail: detail}
	}
	return Outcome{Kind: OutcomeError, Detail: detail}
}

// 아카이브에 남긴 성공 응답 본문(store.Record의 data)을 받았을 때와 같은 방식으로 다시 읽는다.
func SnapshotOf(raw json.RawMessage) (*Snapshot, bool) {
	body, compact := parseBody(raw)
	outcome := Classify(&Response{Status: http.StatusOK, Body: body, Raw: compact})
	return outcome.Snapshot, outcome.Kind == OutcomeOK
}

func rateLimitWait(header http.Header, now time.Time) time.Duration {
	raw := strings.TrimSpace(header.Get("Retry-After"))
	var wait time.Duration
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil && !math.IsInf(seconds, 0) && !math.IsNaN(seconds) {
		wait = time.Duration(min(seconds, MaxWait.Seconds()) * float64(time.Second))
	} else if date, err := http.ParseTime(raw); err == nil {
		wait = date.Sub(now)
	}
	if wait <= 0 {
		return rateLimitDefaultWait
	}
	return min(wait, MaxWait)
}

func errorType(body any) string {
	object, _ := body.(map[string]any)
	detail, _ := object["error"].(map[string]any)
	errType, _ := detail["type"].(string)
	return errType
}

// 요청에 직접 Accept-Encoding을 넣었으므로 압축은 직접 푼다.
func decodeBody(body []byte, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return body, nil
	case "gzip", "x-gzip", "compress", "x-compress", "deflate":
		return unzip(body)
	case "br":
		return io.ReadAll(brotli.NewReader(bytes.NewReader(body)))
	default:
		return nil, fmt.Errorf("지원하지 않는 content-encoding: %s", encoding)
	}
}

// Node의 zlib.unzip처럼 머리 바이트로 gzip인지 zlib(deflate)인지 가린다.
func unzip(body []byte) ([]byte, error) {
	var reader io.ReadCloser
	var err error
	if bytes.HasPrefix(body, []byte{0x1f, 0x8b}) {
		reader, err = gzip.NewReader(bytes.NewReader(body))
	} else {
		reader, err = zlib.NewReader(bytes.NewReader(body))
	}
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func parseBody(decoded []byte) (any, json.RawMessage) {
	var value any
	if err := json.Unmarshal(decoded, &value); err != nil {
		return string(decoded), nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, decoded); err != nil {
		return string(decoded), nil
	}
	return value, compact.Bytes()
}
