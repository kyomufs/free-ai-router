// The free-ai-router command is a gateway that aggregates free AI providers.
//
// WHAT THE ENTRY POINT DOES. It assembles a working daemon out of parts: reads
// the configuration, builds the provider registry, brings up the transport and
// the mechanisms, attaches the surfaces and starts the server. All the logic
// lives in the packages; here there is only the wiring and the command-line
// parsing.
//
// THREE LEVELS OF SETTINGS, in order of precedence:
//
//	this command's flags   beat everything
//	environment variables  override the file
//	config.json            the base layer
//
// OVERRIDING THROUGH FLAGS exists not for convenience, but because the
// declarative system is not always at hand: on NixOS the configuration is owned
// by a module, and changing a setting would mean rebuilding. Flags give the same
// daemon with an unfamiliar setting, touching neither the file nor the module.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// hasFlag reports whether a flag was given.
//
// The check runs BEFORE the parser, on the raw arguments, because it decides
// whether there is a command at all. Only the bare form counts: `--print-config=false`
// is a request NOT to print, and treating it as one would start a daemon in the
// one case where the operator explicitly asked for nothing to happen.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// run parses the command and executes it.
func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("no command given")
	}

	cmd, rest := args[0], args[1:]

	// FLAGS WITHOUT A COMMAND. Exactly one flag means something on its own:
	// --print-config answers a question and exits, so it needs no verb, and
	// demanding `up` in front of it makes the documented form
	// (`free-ai-router --config X --print-config`) fail with "unknown command".
	// Every other flag on its own would START A DAEMON, and one started by
	// accident — because someone typed `--json` alone — is not something to
	// guess at: those still fall through to the usage error below.
	if strings.HasPrefix(cmd, "-") && hasFlag(args, "--print-config") {
		return cmdUp(args)
	}

	switch cmd {
	case "up", "serve", "start":
		return cmdUp(rest)
	case "status":
		return cmdStatus(rest)
	case "stop":
		return cmdStop(rest)
	case "tui":
		return cmdTUI(rest)
	case "models":
		return cmdModels(context.Background(), rest)
	case "stats":
		return cmdStats(rest)
	case "keys":
		return cmdKeys(rest)
	case "logs":
		return cmdLogs(rest)
	case "data":
		return cmdData(rest)
	case "config":
		return cmdConfig(rest)
	case "systemd":
		return cmdSystemd(rest)
	case "axes", "options":
		// `options` is the old name of `axes`. It is kept as an alias rather
		// than as a second implementation: two commands answering the same
		// question is how they end up answering it differently.
		return cmdAxes(rest)
	case "version", "--version", "-v":
		fmt.Println(version())
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	default:
		// The one-axis commands: one field of the configuration, kept.
		if s, ok := findSettable(cmd); ok {
			return cmdSet(s, rest)
		}
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}
