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

// Set at build time with -ldflags "-X github.com/AustinMusiku/timeline/cmd.version=...".
var version = "dev"

var cfgFile string

var rootCmd = &cobra.Command{
	Use:           "timeline",
	Short:         "A network waterfall for the terminal",
	Version:       version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command and exits non-zero on failure.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "timeline:", err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: timeline/config.yaml in the user config dir)")
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
	if err := viper.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			fmt.Fprintln(os.Stderr, "timeline:", err)
			os.Exit(1)
		}
	}
}
