package render

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/AustinMusiku/timeline/internal/record"
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

// minSecret is the shortest value a body hides, so a cookie like lang=en
// doesn't hide every "en".
const minSecret = 8

// credentials returns what redactHeader and redactURL hide in e's request,
// and the values of its response's Set-Cookie.
func credentials(e record.Entry) []string {
	var out []string
	for _, name := range []string{"Authorization", "Proxy-Authorization"} {
		for _, s := range e.Request.Header.Values(name) {
			if _, cred, ok := strings.Cut(s, " "); ok {
				s = cred
			}
			out = append(out, strings.TrimSpace(s))
		}
	}
	for _, s := range e.Request.Header.Values("Cookie") {
		for _, pair := range strings.Split(s, ";") {
			if _, v, ok := strings.Cut(pair, "="); ok {
				out = append(out, strings.TrimSpace(v))
			}
		}
	}
	if u, err := url.Parse(e.Request.URL); err == nil && u.User != nil {
		if p, ok := u.User.Password(); ok {
			out = append(out, p)
		}
	}
	if e.Response != nil {
		for _, c := range (&http.Response{Header: e.Response.Header}).Cookies() {
			out = append(out, c.Value)
		}
	}
	return out
}

// mask hides each secret in body, as written and as JSON escapes it.
func mask(body string, secrets []string) string {
	// Longest first, so a secret inside another leaves none of it showing.
	secrets = slices.SortedFunc(slices.Values(secrets), func(a, b string) int { return len(b) - len(a) })
	for _, s := range secrets {
		if len(s) < minSecret {
			continue
		}
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		enc.Encode(s)
		escaped := strings.TrimSuffix(b.String(), "\n")
		escaped = escaped[1 : len(escaped)-1]
		// Some servers escape / as well.
		for _, form := range []string{s, escaped, strings.ReplaceAll(escaped, "/", `\/`)} {
			body = strings.ReplaceAll(body, form, hidden)
		}
	}
	return body
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
