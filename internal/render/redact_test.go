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

func TestBodiesHideCredentials(t *testing.T) {
	e := record.Entry{
		Request: record.Request{
			Method: "GET", URL: "http://me:pass-word-1@example.com/",
			Header: http.Header{
				"Authorization":       {"Bearer token-12345"},
				"Proxy-Authorization": {"Basic a/b/c/d/e="},
				"Cookie":              {"session=cookie-123; lang=en"},
			},
		},
		Response: &record.Response{
			Status: 200,
			Header: http.Header{"Set-Cookie": {"sid=set-cookie-1; Path=/; HttpOnly"}},
			Content: &record.Content{Body: []byte(`{"auth":"Bearer token-12345","proxy":"a\/b\/c\/d\/e=",` +
				`"cookie":"session=cookie-123; lang=en","pw":"pass-word-1","sid":"set-cookie-1","short":"en"}`)},
		},
	}
	var b strings.Builder
	if err := JSON(&b, e, Options{}); err != nil {
		t.Fatal(err)
	}
	want := `"body":"{\"auth\":\"Bearer xxxxx\",\"proxy\":\"xxxxx\",` +
		`\"cookie\":\"session=xxxxx; lang=en\",\"pw\":\"xxxxx\",\"sid\":\"xxxxx\",\"short\":\"en\"}"`
	if !strings.Contains(b.String(), want) {
		t.Errorf("got  %s\nwant it to have %s", b.String(), want)
	}
}

func TestMaskHidesTheLongerSecretFirst(t *testing.T) {
	if got := mask("abcdefgh-ijkl", []string{"abcdefgh", "abcdefgh-ijkl"}); got != "xxxxx" {
		t.Errorf("got %q, want xxxxx", got)
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
	if err := JSON(&b, e, Options{}); err != nil {
		t.Fatal(err)
	}
	drawn := draw(t, &record.Run{Entries: []record.Entry{e}}, Options{Width: 80})

	for _, got := range []string{b.String(), drawn} {
		if strings.Contains(got, "secret") || !strings.Contains(got, "me:xxxxx@") {
			t.Errorf("the password is not hidden\n%s", got)
		}
	}
}

func TestShowSecretsHidesNothing(t *testing.T) {
	body := `{"token":"token-12345"}`
	e := record.Entry{
		Request: record.Request{
			Method: "GET", URL: "http://me:secret@example.com/",
			Header: http.Header{"Authorization": {"Bearer token-12345"}},
		},
		Response: &record.Response{
			Status:  302,
			Header:  http.Header{"Location": {"https://me:secret@example.com/"}},
			Content: &record.Content{Body: []byte(body), Hide: []string{"token-12345"}},
		},
	}
	opts := Options{Width: 80, ShowSecrets: true}

	var b strings.Builder
	if err := JSON(&b, e, opts); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"url":"http://me:secret@example.com/"`, `"Authorization":["Bearer token-12345"]`,
		`"body":"{\"token\":\"token-12345\"}"`, `"location":"https://me:secret@example.com/"`,
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("the JSON lacks %s\n%s", want, b.String())
		}
	}
	if drawn := draw(t, &record.Run{Entries: []record.Entry{e}}, opts); !strings.Contains(drawn, "me:secret@") {
		t.Errorf("the waterfall hid the password\n%s", drawn)
	}
}
