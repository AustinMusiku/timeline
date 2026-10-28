package cmd

import (
	"bytes"
	"io"
	"os"
	"strings"
)

// dataPart is one value of a body flag.
type dataPart struct {
	value string
}

// dataFlag collects the body flags in the order they were given.
type dataFlag struct {
	parts *[]dataPart
}

func (f dataFlag) String() string { return "" }
func (f dataFlag) Type() string   { return "string" }

func (f dataFlag) Set(v string) error {
	*f.parts = append(*f.parts, dataPart{value: v})
	return nil
}

func (f dataFlag) Append(v string) error { return f.Set(v) }

func (f dataFlag) Replace(vs []string) error {
	*f.parts = (*f.parts)[:0]
	for _, v := range vs {
		f.Set(v)
	}
	return nil
}

func (f dataFlag) GetSlice() []string {
	vs := make([]string, len(*f.parts))
	for i, p := range *f.parts {
		vs[i] = p.value
	}
	return vs
}

// dataParts holds every body flag of the command line.
var dataParts []dataPart

// body joins the parts with &, as curl does, and is nil when there are none.
// @file reads a file, and @- stdin, without their line breaks.
func body(parts []dataPart, stdin io.Reader) ([]byte, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	chunks := make([][]byte, len(parts))
	for i, p := range parts {
		name, ok := strings.CutPrefix(p.value, "@")
		if !ok {
			chunks[i] = []byte(p.value)
			continue
		}
		b, err := readData(name, stdin)
		if err != nil {
			return nil, err
		}
		chunks[i] = []byte(strings.NewReplacer("\r", "", "\n", "").Replace(string(b)))
	}
	b := bytes.Join(chunks, []byte("&"))
	if b == nil {
		b = []byte{} // -d "" is an empty body, not none
	}
	return b, nil
}

func readData(name string, stdin io.Reader) ([]byte, error) {
	if name == "-" {
		return io.ReadAll(stdin)
	}
	return os.ReadFile(name)
}
