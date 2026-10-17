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
//
// EVERY SETTING THE FILE HAS CAN BE SET HERE. That is not convenience: on NixOS
// the configuration is owned by a module, and testing another value would mean
// rebuilding. The flags give the same daemon with an unfamiliar setting while
// touching neither the file nor the module.
type flags struct {
	mode      string
	listen    string
	token     string
	transport string
	upstream  string

	// The mechanism and addressing axes.
	credential string
	rotation   string
	family     string

	// The timeouts, as written on the command line. They are parsed by the
	// configuration, which is where the duration format is known.
	firstEventTimeout    string
	idleTimeout          string
	reasoningIdleTimeout string

	// configPath and statePath move the two files the daemon reads and writes.
	// They are resolved before the file is opened, so they are not part of
	// config.FlagValues.
	configPath string
	statePath  string

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
	fs.StringVar(&f.credential, "credential", "", "what to present: anonymous | account")
	fs.StringVar(&f.rotation, "rotate", "", "rotate the key on a refusal: on | off")
	fs.StringVar(&f.family, "family", "", "address family: auto | v4 | v6")
	fs.StringVar(&f.firstEventTimeout, "first-event-timeout", "", "wait for the first event")
	fs.StringVar(&f.idleTimeout, "idle-timeout", "", "allowed silence between events")
	fs.StringVar(&f.reasoningIdleTimeout, "reasoning-idle-timeout", "", "silence allowed while reasoning")
	fs.StringVar(&f.configPath, "config", "", "configuration file to read instead of the default one")
	fs.StringVar(&f.statePath, "state", "", "state file to write instead of the default one")
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

// values gathers the value flags in the shape the configuration expects.
//
// The path flags are deliberately absent: they are applied to the paths before
// the file is read, so they never travel with the ordinary values.
func (f *flags) values() config.FlagValues {
	return config.FlagValues{
		Mode:                 f.mode,
		Listen:               f.listen,
		Token:                f.token,
		Transport:            f.transport,
		Upstream:             f.upstream,
		Credential:           f.credential,
		Rotation:             f.rotation,
		Family:               f.family,
		FirstEventTimeout:    f.firstEventTimeout,
		IdleTimeout:          f.idleTimeout,
		ReasoningIdleTimeout: f.reasoningIdleTimeout,
	}
}

// pathOverrides reports the path overrides the flags ask for.
func (f *flags) pathOverrides() config.PathOverrides {
	return config.PathOverrides{ConfigFile: f.configPath, StateFile: f.statePath}
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
