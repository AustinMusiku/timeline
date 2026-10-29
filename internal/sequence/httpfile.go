package sequence

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/AustinMusiku/timeline/internal/collect"
)

// ParseHTTP reads a sequence file in the .http format that VS Code's REST
// Client and JetBrains share. name labels errors, and a body read with
// "< path" is found relative to dir.
func ParseHTTP(r io.Reader, name, dir string) ([]collect.Request, error) {
	p := &parser{name: name, dir: dir}
	rd := bufio.NewReader(r)
	for {
		line, err := rd.ReadString('\n')
		if line != "" {
			p.n++
			if err := p.line(line); err != nil {
				return nil, err
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if err := p.finish(); err != nil {
		return nil, err
	}
	return p.reqs, nil
}

// A request is read in three parts, in order.
const (
	preamble = iota // blank lines, comments and # @name, up to the request line
	headers         // the request line's continuation and the headers
	body            // after the first blank line
)

type parser struct {
	name, dir string
	n         int // line number
	reqs      []collect.Request

	part     int
	req      collect.Request
	named    int      // line of # @name, 0 for none
	header   bool     // a header has been read, so the URL is complete
	lines    []string // body lines as written, line breaks included
	file     string   // body file from "< path"
	fileLine int
}

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("%s:%d: %s", p.name, p.n, fmt.Sprintf(format, args...))
}

func (p *parser) line(raw string) error {
	text := strings.TrimRight(raw, "\r\n")
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "###") {
		return p.finish()
	}

	switch p.part {
	case preamble:
		if trimmed == "" {
			return nil
		}
		if c, ok := comment(trimmed); ok {
			return p.directive(c)
		}
		return p.requestLine(trimmed)

	case headers:
		switch {
		case trimmed == "":
			p.part = body
		case !p.header && (trimmed[0] == '?' || trimmed[0] == '&'):
			p.req.URL += trimmed
		case trimmed[0] == '>':
			return p.errorf("response handlers are not supported")
		default:
			if _, ok := comment(trimmed); ok {
				return nil
			}
			name, value, err := ParseHeader(text)
			if err != nil {
				return p.errorf("%v", err)
			}
			p.header = true
			p.req.Header.Add(name, value)
		}
		return nil
	}

	switch {
	case strings.HasPrefix(trimmed, ">"):
		return p.errorf("response handlers are not supported")
	case p.file != "" && trimmed != "":
		return p.errorf("a body read from a file can have nothing else")
	case strings.HasPrefix(trimmed, "< ") && !p.hasBody():
		p.file, p.fileLine = strings.TrimSpace(trimmed[2:]), p.n
	default:
		p.lines = append(p.lines, raw)
	}
	return nil
}

// comment returns the text of a # or // comment.
func comment(s string) (string, bool) {
	if c, ok := strings.CutPrefix(s, "#"); ok {
		return strings.TrimSpace(c), true
	}
	if c, ok := strings.CutPrefix(s, "//"); ok {
		return strings.TrimSpace(c), true
	}
	return "", false
}

// directive reads a comment that starts with @, such as "@name login".
func (p *parser) directive(c string) error {
	d, ok := strings.CutPrefix(c, "@")
	if !ok {
		return nil
	}
	key, value, _ := strings.Cut(d, " ")
	if k, v, ok := strings.Cut(key, "="); ok {
		key, value = k, v
	}
	value = strings.TrimSpace(value)
	if key != "name" {
		return p.errorf("@%s is not supported", key)
	}
	if value == "" {
		return p.errorf("@name has no name")
	}
	p.req.Name, p.named = value, p.n
	return nil
}

// requestLine reads "[METHOD] URL [HTTP/version]".
func (p *parser) requestLine(s string) error {
	fields := strings.Fields(s)
	if len(fields) > 1 && strings.HasPrefix(fields[len(fields)-1], "HTTP/") {
		fields = fields[:len(fields)-1]
	}
	switch len(fields) {
	case 1:
		p.req.Method, p.req.URL = "GET", fields[0]
	case 2:
		if !validName(fields[0]) {
			return p.errorf("%q is not a method", fields[0])
		}
		p.req.Method, p.req.URL = fields[0], fields[1]
	default:
		return p.errorf(`want "METHOD URL", got %d words`, len(fields))
	}
	p.req.Header = http.Header{}
	p.part = headers
	return nil
}

func (p *parser) hasBody() bool {
	for _, l := range p.lines {
		if strings.TrimSpace(l) != "" {
			return true
		}
	}
	return false
}

// finish ends the request being read, if there is one.
func (p *parser) finish() error {
	defer func() { *p = parser{name: p.name, dir: p.dir, n: p.n, reqs: p.reqs} }()
	if p.part == preamble {
		if p.named > 0 {
			p.n = p.named
			return p.errorf("@name %s has no request after it", p.req.Name)
		}
		return nil
	}
	if len(p.req.Header) == 0 {
		p.req.Header = nil
	}

	switch {
	case p.file != "":
		path := p.file
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.dir, path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			p.n = p.fileLine
			return p.errorf("%v", err)
		}
		p.req.Body = b
	case p.hasBody():
		// Blank lines around the body are dropped, then its last line break.
		lines := p.lines
		for strings.TrimSpace(lines[0]) == "" {
			lines = lines[1:]
		}
		for strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		last := len(lines) - 1
		lines[last] = strings.TrimRight(lines[last], "\r\n")
		p.req.Body = []byte(strings.Join(lines, ""))
	}
	p.reqs = append(p.reqs, p.req)
	return nil
}
