// Package wire is the shared wire contract, not tied to a particular provider.
//
// It holds what every provider needs: the identifiers the gateway derives
// itself, and a neutral representation of the request. Service-specific
// detail lives in internal/provider/<name>.
//
// WHAT IS HERE AND WHAT IS THERE. The split follows the question "whose fact
// is this".
//
//	session and message identifiers   here — the gateway derives them
//	the neutral request form          here — the gateway assembles it
//	the set of required tools         at the provider — that is its check
//	the system prompt shape           at the provider — that is its check
//	header names and values           at the provider — that is its contract
//
// The package is pure: no HTTP, no goroutines, no implicit time. That makes it
// testable by properties and fixtures without a network, and the behaviour
// stays fully reproducible.
//
// The identifier formula is derived from the opencode 2.0.30 sources
// (packages/schema/src/identifier.ts and session-message.ts).
package wire
