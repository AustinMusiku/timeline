package cmd

import (
	"crypto/tls"

	"github.com/spf13/pflag"
)

// tlsConfig reads the TLS flags.
func tlsConfig(f *pflag.FlagSet) (*tls.Config, error) {
	insecure, err := f.GetBool("insecure")
	if err != nil {
		return nil, err
	}
	return &tls.Config{InsecureSkipVerify: insecure}, nil
}
