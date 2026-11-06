package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// wants says what a flag of each type takes.
var wants = map[string]string{
	"int":      "a whole number",
	"duration": "a duration such as 5s or 500ms",
	"bool":     "true or false",
}

// curlOnly says what to use in place of curl's flags that timeline lacks.
var curlOnly = []struct{ short, long, instead string }{
	{"L", "location", "timeline already follows redirects, up to --max-redirects"},
	{"s", "silent", "timeline has no progress meter, and always shows errors"},
	{"S", "show-error", "timeline has no progress meter, and always shows errors"},
	{"o", "output", "timeline never prints the body"},
	{"w", "write-out", "--json writes every request's timings"},
	{"v", "verbose", "--json writes every request's headers and connection"},
	{"i", "include", "timeline never prints responses; --json has each one's status"},
	{"I", "head", "-X HEAD sends a HEAD request"},
	{"m", "max-time", "--timeout sets the time limit for each request"},
	{"b", "cookie", "-H 'Cookie: name=value' sends a cookie, and --cookies keeps the ones responses set"},
	{"A", "user-agent", "-H 'User-Agent: ...' sets it"},
}

// flagError rewords pflag's errors to say what the flag wanted, or what to
// use in place of a flag from curl.
func flagError(_ *cobra.Command, err error) error {
	if bad, ok := errors.AsType[*pflag.InvalidValueError](err); ok {
		if want, ok := wants[bad.GetFlag().Value.Type()]; ok {
			return fmt.Errorf("--%s wants %s, not %q", bad.GetFlag().Name, want, bad.GetValue())
		}
	}
	if unknown, ok := errors.AsType[*pflag.NotExistError](err); ok {
		name, short := unknown.GetSpecifiedName(), unknown.GetSpecifiedShortnames() != ""
		for _, c := range curlOnly {
			switch {
			case short && name == c.short:
				return fmt.Errorf("unknown flag -%s: %s", name, c.instead)
			case !short && name == c.long:
				return fmt.Errorf("unknown flag --%s: %s", name, c.instead)
			}
		}
	}
	return err
}
