package sequence

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/AustinMusiku/timeline/internal/collect"
)

// ParseHTTP reads a sequence file in the .http format that VS Code's REST
// Client and JetBrains share. name labels errors, a body read with "< path"
// is found relative to dir, and lookup reads the environment.
func ParseHTTP(r io.Reader, name, dir string, lookup func(string) (string, bool)) ([]collect.Request, error) {
	var lines []string
	rd := bufio.NewReader(r)
	for {
		line, err := rd.ReadString('\n')
		if line != "" {
			lines = append(lines, line)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}

	p := &parser{name: name, dir: dir, lookup: lookup, vars: map[string]variable{}}
	if err := p.definitions(lines); err != nil {
		return nil, err
	}
	for i, line := range lines {
		p.n = i + 1
		if err := p.line(line); err != nil {
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
	preamble = iota // blank lines, comments, # @name and variables, up to the request line
	headers         // the request line's continuation and the headers
	body            // after the first blank line
)

type parser struct {
	name, dir string
	lookup    func(string) (string, bool)
	vars      map[string]variable
	n         int // line number
	reqs      []collect.Request
	cur       step
}

type variable struct {
	value string
	line  int
}

// step is the request being read.
type step struct {
	part     int
	req      collect.Request
	named    int      // line of # @name, 0 for none
	header   bool     // a header has been read, so the URL is complete
	lines    []string // body lines as written, line breaks included
	file     string   // body file from "< path" or "<@ path"
	fileLine int
	expand   bool // fill in the variables in the body file
}

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("%s:%d: %s", p.name, p.n, fmt.Sprintf(format, args...))
}

// definitions reads the file's "@name = value" lines, which sit where
// comments can, so that a variable applies to the whole file.
func (p *parser) definitions(lines []string) error {
	part := preamble
	for i, raw := range lines {
		p.n = i + 1
		trimmed := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(trimmed, "###"):
			part = preamble
		case part == preamble && strings.HasPrefix(trimmed, "@"):
			name, value, ok := strings.Cut(trimmed[1:], "=")
			name = strings.TrimSpace(name)
			if !ok || !validName(name) {
				return p.errorf(`want "@name = value"`)
			}
			if v, ok := p.vars[name]; ok {
				return p.errorf("@%s is already set on line %d", name, v.line)
			}
			p.vars[name] = variable{strings.TrimSpace(value), p.n}
		case part == preamble && trimmed != "":
			if _, ok := comment(trimmed); !ok {
				part = headers
			}
		case part == headers && trimmed == "":
			part = body
		}
	}
	return nil
}

var placeholder = regexp.MustCompile(`\{\{(.*?)\}\}`)

// expand fills in the {{variables}} in s.
func (p *parser) expand(s string) (string, error) {
	return p.expandSeen(s, nil)
}

// expandSeen is expand inside the file variables named in seen.
func (p *parser) expandSeen(s string, seen []string) (string, error) {
	var err error
	out := placeholder.ReplaceAllStringFunc(s, func(m string) string {
		if err != nil {
			return m
		}
		var v string
		v, err = p.value(strings.TrimSpace(m[2:len(m)-2]), seen)
		return v
	})
	return out, err
}

// value looks up one variable. Errors name it but never show a value.
func (p *parser) value(ref string, seen []string) (string, error) {
	if env, ok := strings.CutPrefix(ref, "$processEnv "); ok {
		env = strings.TrimSpace(env)
		if v, ok := p.lookup(env); ok {
			return v, nil
		}
		return "", p.errorf("{{$processEnv %s}} is not set", env)
	}
	switch {
	case strings.HasPrefix(ref, "$"):
		return "", p.errorf("{{%s}} is not supported", strings.Fields(ref)[0])
	case strings.Contains(ref, "."):
		return "", p.errorf("{{%s}} reads an earlier response, which is not supported", ref)
	}
	if v, ok := p.vars[ref]; ok {
		if slices.Contains(seen, ref) {
			return "", p.errorf("@%s refers to itself", ref)
		}
		return p.expandSeen(v.value, append(slices.Clip(seen), ref))
	}
	if v, ok := p.lookup(ref); ok {
		return v, nil
	}
	return "", p.errorf("{{%s}} is not set in the file or the environment", ref)
}

func (p *parser) line(raw string) error {
	text := strings.TrimRight(raw, "\r\n")
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "###") {
		return p.finish()
	}

	c := &p.cur
	switch c.part {
	case preamble:
		if trimmed == "" || strings.HasPrefix(trimmed, "@") {
			return nil
		}
		if text, ok := comment(trimmed); ok {
			return p.directive(text)
		}
		return p.requestLine(trimmed)

	case headers:
		switch {
		case trimmed == "":
			c.part = body
		case !c.header && (trimmed[0] == '?' || trimmed[0] == '&'):
			more, err := p.expand(trimmed)
			if err != nil {
				return err
			}
			c.req.URL += more
		case trimmed[0] == '>':
			return p.errorf("response handlers are not supported")
		default:
			if _, ok := comment(trimmed); ok {
				return nil
			}
			text, err := p.expand(text)
			if err != nil {
				return err
			}
			name, value, err := ParseHeader(text)
			if err != nil {
				return p.errorf("%v", err)
			}
			c.header = true
			c.req.Header.Add(name, value)
		}
		return nil
	}

	switch {
	case strings.HasPrefix(trimmed, ">"):
		return p.errorf("response handlers are not supported")
	case c.file != "" && trimmed != "":
		return p.errorf("a body read from a file can have nothing else")
	case strings.HasPrefix(trimmed, "<@ ") && !c.hasBody():
		c.file, c.fileLine, c.expand = strings.TrimSpace(trimmed[3:]), p.n, true
	case strings.HasPrefix(trimmed, "< ") && !c.hasBody():
		c.file, c.fileLine = strings.TrimSpace(trimmed[2:]), p.n
	default:
		raw, err := p.expand(raw)
		if err != nil {
			return err
		}
		c.lines = append(c.lines, raw)
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
	p.cur.req.Name, p.cur.named = value, p.n
	return nil
}

// requestLine reads "[METHOD] URL [HTTP/version]".
func (p *parser) requestLine(s string) error {
	fields := strings.Fields(s)
	if len(fields) > 1 && strings.HasPrefix(fields[len(fields)-1], "HTTP/") {
		fields = fields[:len(fields)-1]
	}
	method, url := "GET", ""
	switch len(fields) {
	case 1:
		url = fields[0]
	case 2:
		if !validName(fields[0]) {
			return p.errorf("%q is not a method", fields[0])
		}
		method, url = fields[0], fields[1]
	default:
		return p.errorf(`want "METHOD URL", got %d words`, len(fields))
	}
	url, err := p.expand(url)
	if err != nil {
		return err
	}
	c := &p.cur
	c.req.Method, c.req.URL, c.req.Header = method, url, http.Header{}
	c.part = headers
	return nil
}

func (c *step) hasBody() bool {
	for _, l := range c.lines {
		if strings.TrimSpace(l) != "" {
			return true
		}
	}
	return false
}

// finish ends the request being read, if there is one.
func (p *parser) finish() error {
	c := p.cur
	p.cur = step{}
	if c.part == preamble {
		if c.named > 0 {
			p.n = c.named
			return p.errorf("@name %s has no request after it", c.req.Name)
		}
		return nil
	}
	if len(c.req.Header) == 0 {
		c.req.Header = nil
	}

	switch {
	case c.file != "":
		p.n = c.fileLine
		path := c.file
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.dir, path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return p.errorf("%v", err)
		}
		if c.expand {
			s, err := p.expand(string(b))
			if err != nil {
				return err
			}
			b = []byte(s)
		}
		c.req.Body = b
	case c.hasBody():
		// Blank lines around the body are dropped, then its last line break.
		lines := c.lines
		for strings.TrimSpace(lines[0]) == "" {
			lines = lines[1:]
		}
		for strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		last := len(lines) - 1
		lines[last] = strings.TrimRight(lines[last], "\r\n")
		c.req.Body = []byte(strings.Join(lines, ""))
	}
	p.reqs = append(p.reqs, c.req)
	return nil
}
