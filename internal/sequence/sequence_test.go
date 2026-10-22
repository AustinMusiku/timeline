package sequence

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/AustinMusiku/timeline/internal/collect"
)

func TestParseHeader(t *testing.T) {
	tests := []struct {
		line, name, value string
	}{
		{"Accept: text/html", "Accept", "text/html"},
		{"x-api-key:abc", "X-Api-Key", "abc"},
		{"Accept:  a, b ", "Accept", "a, b"},
		{"Authorization: Bearer a:b", "Authorization", "Bearer a:b"},
	}
	for _, tc := range tests {
		name, value, err := ParseHeader(tc.line)
		if err != nil || name != tc.name || value != tc.value {
			t.Errorf("ParseHeader(%q) = %q, %q, %v; want %q, %q", tc.line, name, value, err, tc.name, tc.value)
		}
	}

	for _, line := range []string{"Bearer secret", ": secret", "Bad Name: secret", "Accept:", "X: a\r\nY: secret"} {
		_, _, err := ParseHeader(line)
		if err == nil {
			t.Errorf("ParseHeader(%q) succeeded, want an error", line)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("ParseHeader(%q) error %q gives the value away", line, err)
		}
	}
}

func TestDefaultsGiveWayToTheRequest(t *testing.T) {
	d := Defaults{Header: http.Header{"Accept": {"a"}, "X-Two": {"1", "2"}}}

	got := d.apply(collect.Request{URL: "u", Header: http.Header{"Accept": {"b"}}})
	want := http.Header{"Accept": {"b"}, "X-Two": {"1", "2"}}
	if !reflect.DeepEqual(got.Header, want) {
		t.Errorf("header %v, want %v", got.Header, want)
	}

	reqs := FromURLs([]string{"a", "b"}, d)
	reqs[0].Header.Add("X-Two", "3")
	if !reflect.DeepEqual(reqs[1].Header, d.Header) || len(d.Header["X-Two"]) != 2 {
		t.Errorf("requests share their headers: %v, %v", reqs[1].Header, d.Header)
	}
}
