package wire

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	diagnosticHTTPURL  = regexp.MustCompile(`(?i)\bhttps?://[^\s<>"'\x60]+`)
	diagnosticUserinfo = regexp.MustCompile(`(?i)(\bhttps?://)[^\s/?#<>"'\x60]*@`)
	diagnosticURLParam = regexp.MustCompile(`(^|[&#;])[^&#;=]+=[^&#;]*`)
)

// RedactURLCredentials removes userinfo and named credential values from HTTP
// URLs embedded in diagnostics. It keeps endpoint paths, ordinary parameters,
// and the surrounding diagnostic intact.
func RedactURLCredentials(text string) string {
	if !strings.Contains(text, "://") {
		return text
	}
	// Match userinfo directly so malformed or nested URLs in forwarded
	// diagnostics are also covered.
	if strings.Contains(text, "@") {
		text = diagnosticUserinfo.ReplaceAllString(text, "${1}")
	}
	return diagnosticHTTPURL.ReplaceAllStringFunc(text, func(raw string) string {
		paramStart := strings.IndexAny(raw, "?#")
		if paramStart == -1 {
			return raw
		}
		paramStart++
		return raw[:paramStart] + diagnosticURLParam.ReplaceAllStringFunc(raw[paramStart:], redactURLParameter)
	})
}

func redactURLParameter(pair string) string {
	equals := strings.IndexByte(pair, '=')
	if equals+1 == len(pair) {
		return pair
	}
	key, err := url.QueryUnescape(strings.TrimLeft(pair[:equals], "&#;"))
	if err != nil {
		return pair
	}
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
	switch key {
	case "codexprotoken", "token", "accesstoken", "authtoken", "refreshtoken", "idtoken", "sessiontoken",
		"apitoken", "apikey", "key", "credential", "credentials", "authorization", "password", "secret",
		"clientsecret", "bearer", "sig", "signature", "xamzcredential", "xamzsecuritytoken", "xamzsignature",
		"xgoogcredential", "xgoogsignature":
		return pair[:equals+1] + redactedSecret
	default:
		return pair
	}
}
