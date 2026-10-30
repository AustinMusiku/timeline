// Package sequence builds the requests of a run.
package sequence

import (
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/AustinMusiku/timeline/internal/collect"
)

// Defaults apply to every request of a sequence. A request's own values win.
type Defaults struct {
	Method string
	Header http.Header
	Body   []byte
}

// FromURLs returns a request for each URL, with d applied.
func FromURLs(urls []string, d Defaults) []collect.Request {
	reqs := make([]collect.Request, len(urls))
	for i, u := range urls {
		reqs[i] = d.Apply(collect.Request{URL: u})
	}
	return reqs
}

// Apply fills in what r leaves out.
func (d Defaults) Apply(r collect.Request) collect.Request {
	if r.Method == "" {
		r.Method = d.Method
	}
	if r.Body == nil {
		r.Body = d.Body
	}
	for name, values := range d.Header {
		if _, ok := r.Header[name]; ok {
			continue
		}
		if r.Header == nil {
			r.Header = http.Header{}
		}
		r.Header[name] = slices.Clone(values)
	}
	return r
}

// BasicAuth is the Authorization value for user and password.
func BasicAuth(user, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
}

// ParseHeader splits a "Name: value" line. Errors leave the value out, since
// it may be a secret.
func ParseHeader(line string) (name, value string, err error) {
	name, value, ok := strings.Cut(line, ":")
	if !ok {
		return "", "", errors.New(`not "Name: value"`)
	}
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if !validName(name) {
		return "", "", errors.New(`not "Name: value"`)
	}
	if value == "" {
		return "", "", errors.New(name + " has no value")
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", "", errors.New(name + " has a line break in its value")
	}
	return http.CanonicalHeaderKey(name), value, nil
}

// validName reports whether s is an HTTP token.
func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range []byte(s) {
		alnum := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
		if !alnum && !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			return false
		}
	}
	return true
}
