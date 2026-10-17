package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"free-ai-router/internal/config"
)

// flags — the parsed command-line arguments.
//
// The defaults are empty strings, not values from the configuration. An empty
// string means "flag not set", and that matters: otherwise a run without flags
// would overwrite the configuration with defaults.
type flags struct {
	mode      string
	listen    string
	token     string
	transport string
	upstream  string

	// json enables machine-readable output.
	json bool
	// printConfig prints the effective configuration and exits.
	printConfig bool
	// detach starts the daemon in the background.
	detach bool

	// Positional arguments left over after parsing.
	positional []string
}

// parseFlags parses the arguments.
//
// Parsing is done by hand, not through flag.Parse: the commands need DIFFERENT
// sets of flags, and a common set would force accepting what a command does not
// need.
func parseFlags(args []string) (*flags, error) {
	f := &flags{}
	fs := flag.NewFlagSet("free-ai-router", flag.ContinueOnError)
	// We print the errors ourselves: flag has its own message, and it would
	// duplicate ours.
	fs.SetOutput(io.Discard)

	fs.StringVar(&f.mode, "mode", "", "deployment form: local | server")
	fs.StringVar(&f.listen, "listen", "", "listen address")
	fs.StringVar(&f.token, "token", "", "access token for server mode")
	fs.StringVar(&f.transport, "transport", "", "transport: direct")
	fs.StringVar(&f.upstream, "upstream", "", "default address for providers")
	fs.BoolVar(&f.json, "json", false, "machine-readable output")
	fs.BoolVar(&f.printConfig, "print-config", false, "show the effective configuration and exit")
	fs.BoolVar(&f.detach, "detach", false, "start the daemon in the background")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil, nil
		}
		return nil, fmt.Errorf("parsing arguments: %w", err)
	}
	f.positional = fs.Args()
	return f, nil
}

// applyToFlags applies the parsed flags to the configuration.
func (f *flags) applyToConfig(c interface {
	ApplyFlags(mode, listen, token, transport, upstream string)
}) {
	c.ApplyFlags(f.mode, f.listen, f.token, f.transport, f.upstream)
}

// joinArgs assembles a description of the remaining arguments for an error
// message.
func joinArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return strings.Join(args, " ")
}

// loadConfigSimple reads the configuration with the flags, but without parsing
// them into a variable: commands that need the flags only as an override do not
// require access to the values themselves.
func loadConfigSimple(args []string) (*config.Config, error) {
	cfg, _, err := loadConfig(args)
	return cfg, err
}
