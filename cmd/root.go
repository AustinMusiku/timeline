package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

// configErr is why the config file could not be read, if it couldn't.
var configErr error

var rootCmd = &cobra.Command{
	Use:           "timeline",
	Short:         "A network waterfall for the terminal",
	Version:       buildVersion(),
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(*cobra.Command, []string) error {
		return configErr
	},
}

// Execute runs the root command and exits with its status.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "timeline:", err)
		os.Exit(exitStatus(err))
	}
}

// exitError is an error from a run that sent requests.
type exitError struct {
	status int
	err    error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// exitStatus is 2 for any error that came before a request was sent.
func exitStatus(err error) int {
	if err == nil {
		return 0
	}
	if e, ok := errors.AsType[*exitError](err); ok {
		return e.status
	}
	return 2
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.SetFlagErrorFunc(flagError)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: timeline/config.yaml in the user config dir)")
	rootCmd.PersistentFlags().String("color", "auto", "when to use colour: auto, always or never")
	viper.BindPFlag("color", rootCmd.PersistentFlags().Lookup("color"))
}

// Settings are resolved in this order: flag, TIMELINE_* environment
// variable, config file, default.
func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else if dir, err := os.UserConfigDir(); err == nil {
		viper.AddConfigPath(filepath.Join(dir, "timeline"))
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
	}

	viper.SetEnvPrefix("TIMELINE")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()

	// A missing config file is just fine
	configErr = nil
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := errors.AsType[viper.ConfigFileNotFoundError](err); !ok {
			from := viper.ConfigFileUsed()
			if cfgFile != "" {
				from = "--config"
			}
			configErr = fmt.Errorf("%s: %w", from, err)
		}
	}
}
