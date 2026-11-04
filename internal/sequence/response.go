package sequence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/AustinMusiku/timeline/internal/collect"
)

// responsePath picks a value out of a response: "body.a.b[0]", "body.$.a",
// "body.*" or "headers.Name".
type responsePath struct {
	header string
	whole  bool
	steps  []pathStep
}

type pathStep struct {
	key   string
	index int // used when key is empty
}

func (s pathStep) String() string {
	if s.key == "" {
		return fmt.Sprintf("[%d]", s.index)
	}
	return s.key
}

func parsePath(s string) (responsePath, error) {
	if name, ok := strings.CutPrefix(s, "headers."); ok && name != "" {
		return responsePath{header: name}, nil
	}
	rest, ok := strings.CutPrefix(s, "body.")
	if !ok {
		return responsePath{}, errors.New(`want body.<path> or headers.<name>`)
	}
	rest = strings.TrimPrefix(strings.TrimPrefix(rest, "$"), ".")
	if rest == "" || rest == "*" {
		return responsePath{whole: true}, nil
	}

	var p responsePath
	for seg := range strings.SplitSeq(rest, ".") {
		key, idx, _ := strings.Cut(seg, "[")
		if key == "" && idx == "" {
			return responsePath{}, fmt.Errorf("%q has an empty part", rest)
		}
		if key != "" {
			p.steps = append(p.steps, pathStep{key: key})
		}
		for idx != "" {
			n, more, ok := strings.Cut(idx, "]")
			i, err := strconv.Atoi(n)
			if !ok || err != nil || i < 0 || (more != "" && more[0] != '[') {
				return responsePath{}, fmt.Errorf("%q has a bad index", rest)
			}
			p.steps = append(p.steps, pathStep{index: i})
			idx = strings.TrimPrefix(more, "[")
		}
	}
	return p, nil
}

// value reads the path out of r. Errors never show what r holds.
func (p responsePath) value(r *collect.Response) (string, error) {
	if p.header != "" {
		if v := r.Header.Get(p.header); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("has no %s header", p.header)
	}
	if r.BodyErr != nil {
		return "", r.BodyErr
	}
	if p.whole {
		return string(r.Body), nil
	}

	dec := json.NewDecoder(bytes.NewReader(r.Body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", errors.New("the body is not JSON")
	}
	for i, s := range p.steps {
		at := pathString(p.steps[:i+1])
		switch node := v.(type) {
		case map[string]any:
			if s.key == "" {
				return "", fmt.Errorf("%s is not a list", pathString(p.steps[:i]))
			}
			var ok bool
			if v, ok = node[s.key]; !ok {
				return "", fmt.Errorf("the body has no %s", at)
			}
		case []any:
			if s.key != "" {
				return "", fmt.Errorf("the body has no %s", at)
			}
			if s.index >= len(node) {
				return "", fmt.Errorf("the body has no %s", at)
			}
			v = node[s.index]
		default:
			return "", fmt.Errorf("the body has no %s", at)
		}
	}

	switch v := v.(type) {
	case nil:
		return "", fmt.Errorf("%s is null", pathString(p.steps))
	case string:
		return v, nil
	case json.Number:
		return v.String(), nil
	default:
		b, err := json.Marshal(v)
		return string(b), err
	}
}

func pathString(steps []pathStep) string {
	var b strings.Builder
	for i, s := range steps {
		if i > 0 && s.key != "" {
			b.WriteByte('.')
		}
		b.WriteString(s.String())
	}
	return b.String()
}
