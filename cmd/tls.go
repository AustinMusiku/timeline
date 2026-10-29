package cmd

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// tlsConfig reads the TLS flags.
func tlsConfig(f *pflag.FlagSet) (*tls.Config, error) {
	insecure, err := f.GetBool("insecure")
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{InsecureSkipVerify: insecure}

	// As in curl, the file takes the place of the system's roots.
	if file := viper.GetString("cacert"); file != "" {
		pem, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("--cacert: %w", err)
		}
		cfg.RootCAs = x509.NewCertPool()
		if !cfg.RootCAs.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("--cacert: no certificates in %s", file)
		}
	}
	return cfg, nil
}
