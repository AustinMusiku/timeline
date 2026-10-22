package render

import (
	"net/http"
	"net/url"
	"strings"
)

const hidden = "xxxxx"

// redactHeader returns a copy of h with credentials hidden. An
// Authorization keeps its scheme and a Cookie its names.
func redactHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for name, values := range h {
		v := make([]string, len(values))
		for i, s := range values {
			switch name {
			case "Authorization", "Proxy-Authorization":
				if scheme, _, ok := strings.Cut(s, " "); ok {
					s = scheme + " " + hidden
				} else {
					s = hidden
				}
			case "Cookie":
				pairs := strings.Split(s, ";")
				for j, p := range pairs {
					name, _, _ := strings.Cut(p, "=")
					pairs[j] = name + "=" + hidden
				}
				s = strings.Join(pairs, ";")
			}
			v[i] = s
		}
		out[name] = v
	}
	return out
}

// redactURL hides the password in s, if it has one.
func redactURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.User == nil {
		return s
	}
	if _, ok := u.User.Password(); !ok {
		return s
	}
	return u.Redacted()
}
