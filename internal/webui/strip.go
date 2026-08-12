package webui

import (
	"net/http"
	"strings"
)

// Platform-reserved cookie prefixes. Anything matching is removed before a
// request reaches untrusted upstream code; everything else (the previewed
// application's own cookies) is passed through untouched.
var reservedCookiePrefixes = []string{
	"agentcell_", // console session + per-Cell preview tickets
	"casdoor",    // control-plane SSO session, when fronted by Casdoor
	"apisix_",    // control-plane gateway session
	"CASTGC",     // CAS ticket-granting cookie
	"JSESSIONID", // common gateway/SSO session name
}

func isReservedCookie(name string) bool {
	for _, p := range reservedCookiePrefixes {
		if strings.HasPrefix(name, p) || strings.EqualFold(name, p) {
			return true
		}
	}
	return false
}

// stripPlatformCredentials removes every credential the control plane (or
// a gateway in front of it) may have attached, so untrusted upstreams never
// observe them. Called on the proxy's outbound request.
func stripPlatformCredentials(req *http.Request) {
	req.Header.Del("Authorization")
	req.Header.Del("X-Forwarded-Access-Token")
	req.Header.Del("X-Auth-Request-Access-Token")

	cookies := req.Cookies()
	req.Header.Del("Cookie")
	kept := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if isReservedCookie(c.Name) {
			continue
		}
		kept = append(kept, c.Name+"="+c.Value)
	}
	if len(kept) > 0 {
		req.Header.Set("Cookie", strings.Join(kept, "; "))
	}
}
