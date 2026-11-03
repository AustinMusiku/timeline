package sequence

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/AustinMusiku/timeline/internal/collect"
)

func TestResponseValues(t *testing.T) {
	resp := &collect.Response{
		Status: 200,
		Header: http.Header{"X-Request-Id": {"abc"}},
		Body: []byte(`{"access_token": "tok", "expires": 3600, "big": 12345678901234567890,
			"ok": true, "data": {"items": [{"id": 7}, {"id": 8, "tags": ["a", "b"]}]}, "gone": null}`),
	}
	tests := map[string]string{
		"body.access_token":          "tok",
		"body.$.access_token":        "tok",
		"body.expires":               "3600",
		"body.big":                   "12345678901234567890",
		"body.ok":                    "true",
		"body.data.items[1].id":      "8",
		"body.$.data.items[1].tags":  `["a","b"]`,
		"body.data.items[1].tags[0]": "a",
		"body.data.items[0]":         `{"id":7}`,
		"headers.X-Request-Id":       "abc",
		"headers.x-request-id":       "abc",
	}
	for path, want := range tests {
		p, err := parsePath(path)
		if err != nil {
			t.Errorf("parsePath(%q): %v", path, err)
			continue
		}
		if got, err := p.value(resp); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", path, got, err, want)
		}
	}

	for _, path := range []string{"body.*", "body.$"} {
		p, _ := parsePath(path)
		if got, err := p.value(resp); err != nil || got != string(resp.Body) {
			t.Errorf("%s = %q, %v; want the whole body", path, got, err)
		}
	}
}

func TestResponseValueErrors(t *testing.T) {
	resp := &collect.Response{Body: []byte(`{"secret": "s3cr3t", "list": [1], "gone": null}`), Header: http.Header{}}
	tests := map[string]string{
		"body.missing":      "the body has no missing",
		"body.secret.inner": "the body has no secret.inner",
		"body.list[1]":      "the body has no list[1]",
		"body.list.x":       "the body has no list.x",
		"body.secret[0]":    "the body has no secret[0]",
		"body.gone":         "gone is null",
		"headers.X-Nope":    "has no X-Nope header",
	}
	for path, want := range tests {
		p, err := parsePath(path)
		if err != nil {
			t.Errorf("parsePath(%q): %v", path, err)
			continue
		}
		_, err = p.value(resp)
		if err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", path, err, want)
		} else if strings.Contains(err.Error(), "s3cr3t") {
			t.Errorf("%s: error %q gives the body away", path, err)
		}
	}

	p, _ := parsePath("body.token")
	if _, err := p.value(&collect.Response{Body: []byte("<html>s3cr3t</html>")}); err == nil || err.Error() != "the body is not JSON" {
		t.Errorf("an HTML body: error %v, want the body is not JSON", err)
	}
	broken := errors.New("the body is br-encoded")
	if _, err := p.value(&collect.Response{BodyErr: broken}); !errors.Is(err, broken) {
		t.Errorf("an unreadable body: error %v, want %v", err, broken)
	}
}

func TestParsePathErrors(t *testing.T) {
	for _, path := range []string{"status", "body", "headers.", "body.a..b", "body.a[x]", "body.a[1", "body.a[-1]", "body.a[0]b"} {
		if _, err := parsePath(path); err == nil {
			t.Errorf("parsePath(%q) succeeded, want an error", path)
		}
	}
}
