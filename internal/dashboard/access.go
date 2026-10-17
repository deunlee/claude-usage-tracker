package dashboard

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

type Access struct {
	// 비어 있으면 Authorization 헤더 없이 보낸 요청을 받는다.
	Password string
	// 여기에 드는 클라이언트는 비밀번호 없이 API를 쓴다.
	AllowedIPs []netip.Prefix
	// 아무나 X-Forwarded-For를 꾸며 허용 IP인 척하지 못하게, 이 주소에서 온 연결의 헤더만 믿는다.
	TrustedProxies []netip.Prefix
}

// 로그인은 허용 IP에서도 비밀번호를 검사한다. 틀린 비밀번호를 저장한 브라우저가 허용 IP 밖에서 401을 받게 되는 일을 막는다.
func (d *Server) authorized(r *http.Request) bool {
	if r.URL.Path != "/api/login" && d.ipAllowed(r) {
		return true
	}
	return d.passwordMatches(r)
}

// 토큰은 base64로 인코딩한 비밀번호다. 영문·숫자 밖의 글자가 든 비밀번호도 헤더에 넣을 수 있다.
func (d *Server) passwordMatches(r *http.Request) bool {
	token := ""
	if header := r.Header.Get("Authorization"); header != "" {
		var ok bool
		if token, ok = strings.CutPrefix(header, "Bearer "); !ok {
			return false
		}
	}
	password, err := base64.StdEncoding.DecodeString(token)
	return err == nil && subtle.ConstantTimeCompare(password, []byte(d.access.Password)) == 1
}

func (d *Server) ipAllowed(r *http.Request) bool {
	if len(d.access.AllowedIPs) == 0 {
		return false
	}
	client, ok := clientAddr(r, d.access.TrustedProxies)
	return ok && containsAddr(d.access.AllowedIPs, client)
}

// 프록시마다 받은 연결의 주소를 X-Forwarded-For 뒤에 붙이므로, 오른쪽부터 믿는 프록시를 건너뛴 첫 주소가 클라이언트다.
// 앞쪽 값은 클라이언트가 마음대로 적을 수 있어 그 너머는 보지 않는다. 주소로 읽을 수 없는 값을 만나면 클라이언트를 모르는 것으로 본다.
func clientAddr(r *http.Request, trustedProxies []netip.Prefix) (netip.Addr, bool) {
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	client := normalizeAddr(remote.Addr())
	if !containsAddr(trustedProxies, client) {
		return client, true
	}
	var hops []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(header, ",")...)
	}
	for _, hop := range slices.Backward(hops) {
		addr, err := netip.ParseAddr(strings.TrimSpace(hop))
		if err != nil {
			return netip.Addr{}, false
		}
		client = normalizeAddr(addr)
		if !containsAddr(trustedProxies, client) {
			return client, true
		}
	}
	return client, true
}

// 듀얼 스택 소켓은 IPv4 연결을 ::ffff:a.b.c.d로 주고, 존이 붙은 주소는 Prefix.Contains가 늘 false라 둘 다 떼어 낸다.
func normalizeAddr(addr netip.Addr) netip.Addr {
	return addr.Unmap().WithZone("")
}

func containsAddr(prefixes []netip.Prefix, addr netip.Addr) bool {
	return slices.ContainsFunc(prefixes, func(p netip.Prefix) bool { return p.Contains(addr) })
}
