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
)

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
	case "options":
		return cmdOptions(rest)
	case "version", "--version", "-v":
		fmt.Println(version())
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}
