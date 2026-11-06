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

// flagError rewords pflag's errors to say what the flag wanted.
func flagError(_ *cobra.Command, err error) error {
	if bad, ok := errors.AsType[*pflag.InvalidValueError](err); ok {
		if want, ok := wants[bad.GetFlag().Value.Type()]; ok {
			return fmt.Errorf("--%s wants %s, not %q", bad.GetFlag().Name, want, bad.GetValue())
		}
	}
	return err
}
