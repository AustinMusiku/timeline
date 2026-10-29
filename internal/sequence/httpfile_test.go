package sequence

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AustinMusiku/timeline/internal/collect"
)

// env is the environment the tests see.
func env(name string) (string, bool) {
	v, ok := map[string]string{"TOKEN": "secret", "HOST": "api.example.com"}[name]
	return v, ok
}

func parse(t *testing.T, file string) ([]collect.Request, error) {
	t.Helper()
	return ParseHTTP(strings.NewReader(file), "flow.http", t.TempDir(), env)
}

func TestParseHTTP(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "avatar.png"), []byte("\x89PNG\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := `# a comment
// another

# @name login
POST https://api.example.com/login HTTP/1.1
Content-Type: application/json

{"user": "me"}

###
GET https://api.example.com/search
  ?q=go
  &page=2
Accept: text/html
###   a title, ignored
https://api.example.com/plain

###
// @name=upload
PUT https://api.example.com/avatar
Content-Type: image/png

< ./avatar.png
`
	got, err := ParseHTTP(strings.NewReader(file), "flow.http", dir, env)
	if err != nil {
		t.Fatal(err)
	}
	want := []collect.Request{
		{
			Name: "login", Method: "POST", URL: "https://api.example.com/login",
			Header: http.Header{"Content-Type": {"application/json"}},
			Body:   []byte(`{"user": "me"}`),
		},
		{
			Method: "GET", URL: "https://api.example.com/search?q=go&page=2",
			Header: http.Header{"Accept": {"text/html"}},
		},
		{Method: "GET", URL: "https://api.example.com/plain"},
		{
			Name: "upload", Method: "PUT", URL: "https://api.example.com/avatar",
			Header: http.Header{"Content-Type": {"image/png"}},
			Body:   []byte("\x89PNG\r\n"),
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestParseHTTPBodies(t *testing.T) {
	tests := []struct {
		name, file string
		want       []byte
	}{
		{"line breaks kept as written", "POST https://a.example\r\n\r\nline1\r\nline2\r\n\r\n###\r\n", []byte("line1\r\nline2")},
		{"blank lines around dropped", "POST https://a.example\n\n\na\n\nb\n\n\n", []byte("a\n\nb")},
		{"no body", "POST https://a.example\nAccept: */*\n\n\n", nil},
		{"a # line is part of the body", "POST https://a.example\n\n# heading\ntext", []byte("# heading\ntext")},
	}
	for _, tc := range tests {
		got, err := parse(t, tc.file)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if len(got) != 1 || !reflect.DeepEqual(got[0].Body, tc.want) {
			t.Errorf("%s: got %+v, want one request with body %q", tc.name, got, tc.want)
		}
	}
}

func TestParseHTTPWithoutRequests(t *testing.T) {
	for _, file := range []string{"", "\n\n", "# just a comment\n###\n// and another\n"} {
		if got, err := parse(t, file); err != nil || len(got) != 0 {
			t.Errorf("%q: got %v and error %v, want no requests", file, got, err)
		}
	}
}

func TestParseHTTPErrors(t *testing.T) {
	tests := []struct{ file, want string }{
		{"# @no-redirect\nGET https://a.example", "flow.http:1: @no-redirect is not supported"},
		{"GET https://a.example\n\n{}\n> {% client.global.set('t', response.body.token) %}", "flow.http:4: response handlers are not supported"},
		{"GET https://a.example\n>> out.json", "flow.http:2: response handlers are not supported"},
		{"GET https://a.example\nAuthorization Bearer secret", `flow.http:2: not "Name: value"`},
		{"# @name lonely\n###\nGET https://a.example", "flow.http:1: @name lonely has no request after it"},
		{"# @name", "flow.http:1: @name has no name"},
		{"GET https://a.example now please", `flow.http:1: want "METHOD URL", got 4 words`},
		{"BAD/METHOD https://a.example", `flow.http:1: "BAD/METHOD" is not a method`},
		{"\n\nPOST https://a.example\n\n< ./missing.json", "flow.http:5: open "},
		{"POST https://a.example\n\n< ./a.json\nmore", "flow.http:4: a body read from a file can have nothing else"},
	}
	for _, tc := range tests {
		_, err := parse(t, tc.file)
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Errorf("%q: error %v, want one starting %q", tc.file, err, tc.want)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("%q: error %q gives a value away", tc.file, err)
		}
	}
}

func TestParseHTTPVariables(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "body.json"), []byte(`{"token": "{{$processEnv TOKEN}}"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	file := `@base = https://{{HOST}}/{{version}}

POST {{base}}/login
  ?as={{user}}
Authorization: Bearer {{$processEnv TOKEN}}

{"user": "{{user}}"}

###
POST {{base}}/raw

< ./body.json

###
POST {{base}}/filled

<@ ./body.json

###
@version = v1
@user = me
`
	got, err := ParseHTTP(strings.NewReader(file), "flow.http", dir, env)
	if err != nil {
		t.Fatal(err)
	}
	want := []collect.Request{
		{
			Method: "POST", URL: "https://api.example.com/v1/login?as=me",
			Header: http.Header{"Authorization": {"Bearer secret"}},
			Body:   []byte(`{"user": "me"}`),
		},
		{Method: "POST", URL: "https://api.example.com/v1/raw", Body: []byte(`{"token": "{{$processEnv TOKEN}}"}`)},
		{Method: "POST", URL: "https://api.example.com/v1/filled", Body: []byte(`{"token": "secret"}`)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestParseHTTPVariableErrors(t *testing.T) {
	tests := []struct{ file, want string }{
		{"GET https://a.example/{{missing}}", "flow.http:1: {{missing}} is not set in the file or the environment"},
		{"GET https://a.example\nX-Key: {{$processEnv MISSING}}", "flow.http:2: {{$processEnv MISSING}} is not set"},
		{"GET https://a.example/{{$guid}}", "flow.http:1: {{$guid}} is not supported"},
		{"GET https://a.example\nAuthorization: Bearer {{login.response.body.$.token}}", "flow.http:2: {{login.response.body.$.token}} reads an earlier response"},
		{"@a = {{b}}\n@b = {{a}}\nGET https://a.example/{{a}}", "flow.http:3: @a refers to itself"},
		{"@a = 1\n@a = 2\nGET https://a.example", "flow.http:2: @a is already set on line 1"},
		{"@a\nGET https://a.example", `flow.http:1: want "@name = value"`},
		{"POST https://a.example\n\n{{TOKEN}} and {{nope}}", "flow.http:3: {{nope}} is not set"},
	}
	for _, tc := range tests {
		_, err := parse(t, tc.file)
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Errorf("%q: error %v, want one starting %q", tc.file, err, tc.want)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("%q: error %q gives a value away", tc.file, err)
		}
	}
}
