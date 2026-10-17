package main

import (
	"fmt"
	"os"

	"free-ai-router/internal/axes"
	"free-ai-router/internal/catalog"
)

// dictionaryRegistries returns the registries the dictionary is filled from.
//
// The provider types come from the assembly table and the transports from their
// own registry, so the dictionary never lists a name that does not exist. The
// dictionary itself imports neither: it describes the settings, and reaching for
// the implementations would drag every provider into a command that answers
// without any of them.
func dictionaryRegistries() axes.Registries {
	return axes.Registries{
		Providers:  catalog.KnownTypes(),
		Transports: transportIDs(),
	}
}

// axisExists reports whether the dictionary describes a setting under this key.
//
// The one-axis subcommands are checked against it: a subcommand writing a key the
// dictionary does not describe would be a setting the NixOS module cannot verify,
// which is how a module drifts away from its binary.
func axisExists(configKey string) bool {
	for _, a := range axes.Catalog(dictionaryRegistries()) {
		if a.ConfigKey == configKey {
			return true
		}
	}
	return false
}

// cmdAxes prints the dictionary of settings.
//
// WHY IT EXISTS. One and the same set of settings is declared in three places
// that have to agree: the daemon, the command line, and the NixOS module. Written
// three times, the lists drift apart, and the drift is silent — an option the
// binary does not know about simply does nothing. This command is the one place
// they are written down, and the module's checks are run against its output.
//
// `--json` is the machine-readable form; the module reads the value lists from
// it instead of repeating them.
func cmdAxes(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if f == nil {
		return nil
	}

	// The provider types come from the assembly table and the transports from
	// their own registry, so the dictionary never lists a name that does not
	// exist. The dictionary does not import either: it describes the settings,
	// and reaching for the implementations would mean dragging every provider
	// into a command that answers without any of them.
	r := dictionaryRegistries()

	if f.json {
		raw, err := axes.JSON(r)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(raw)
		return err
	}

	fmt.Print(axes.Text(r))
	return nil
}
