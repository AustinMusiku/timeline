package render

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/AustinMusiku/timeline/internal/record"
)

func TestRedactURL(t *testing.T) {
	tests := map[string]string{
		"https://me:secret@example.com/a?b=1": "https://me:xxxxx@example.com/a?b=1",
		"https://me@example.com/":             "https://me@example.com/",
		"https://example.com/a%2Fb":           "https://example.com/a%2Fb",
		"/relative":                           "/relative",
		"http://me:secret@[::1":               "http://me:secret@[::1",
	}
	for in, want := range tests {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactHeader(t *testing.T) {
	h := http.Header{
		"Authorization":       {"Bearer secret"},
		"Proxy-Authorization": {"secret"},
		"Cookie":              {"session=secret; theme=secret", "lone"},
		"Accept":              {"text/html", "*/*"},
	}
	want := http.Header{
		"Authorization":       {"Bearer xxxxx"},
		"Proxy-Authorization": {"xxxxx"},
		"Cookie":              {"session=xxxxx; theme=xxxxx", "lone=xxxxx"},
		"Accept":              {"text/html", "*/*"},
	}
	if got := redactHeader(h); !reflect.DeepEqual(got, want) {
		t.Errorf("redactHeader(%v)\n got %v\nwant %v", h, got, want)
	}
	if h.Get("Authorization") != "Bearer secret" {
		t.Errorf("redactHeader changed the header it was given")
	}
	if got := redactHeader(nil); got == nil || len(got) != 0 {
		t.Errorf("redactHeader(nil) = %#v, want an empty header", got)
	}
}

func TestPasswordsAreHidden(t *testing.T) {
	e := record.Entry{
		Request: record.Request{Method: "GET", URL: "http://me:secret@example.com/"},
		Response: &record.Response{
			Status: 302,
			Header: http.Header{"Location": {"https://me:secret@example.com/"}},
		},
	}

	var b strings.Builder
	if err := JSON(&b, e); err != nil {
		t.Fatal(err)
	}
	drawn := draw(t, &record.Run{Entries: []record.Entry{e}}, Options{Width: 80})

	for _, got := range []string{b.String(), drawn} {
		if strings.Contains(got, "secret") || !strings.Contains(got, "me:xxxxx@") {
			t.Errorf("the password is not hidden\n%s", got)
		}
	}
}
