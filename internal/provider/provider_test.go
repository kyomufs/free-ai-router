package provider

import (
	"context"
	"net/http"
	"testing"
)

// standIn is a provider that answers nothing of its own.
//
// The registry is common machinery, so what it is asked here is only how it
// keeps providers — the stand-in exists to be registered and found, and a real
// provider would drag in a transport and a catalog that the registry never
// touches.
type standIn struct{ id ID }

func (s standIn) ID() ID { return s.id }

func (s standIn) Models(context.Context) ([]Model, error) { return nil, nil }

func (s standIn) Build(Request) (Prepared, error) { return Prepared{}, nil }

func (s standIn) Classify(resp *http.Response, body []byte) Failure {
	return Failure{Status: resp.StatusCode}
}

// TestRegisteredProvidersComeBackInIdentifierOrder — the identifiers are kept
// sorted rather than in registration order.
//
// Configuration may be written in any order and reloaded at any time, so a
// listing that followed the order of the file would reshuffle under the operator
// between two runs with no change to the set.
func TestRegisteredProvidersComeBackInIdentifierOrder(t *testing.T) {
	r := NewRegistry()
	r.Register(standIn{id: "zen"})
	r.Register(standIn{id: "alpha"})
	r.Register(standIn{id: "mid"})

	got := r.IDs()
	want := []ID{"alpha", "mid", "zen"}
	if len(got) != len(want) {
		t.Fatalf("IDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("IDs = %v, want %v", got, want)
		}
	}
	if r.Len() != len(want) {
		t.Fatalf("Len = %d, want %d", r.Len(), len(want))
	}
}

// TestReRegisteringKeepsTheOriginalPosition — a hot configuration reload that
// replaces a provider must not move it in the listing.
//
// The case is real: a reload re-registers every provider, and if replacement
// appended instead of overwriting, the listing would flip between runs while
// nothing the operator wrote had changed.
func TestReRegisteringKeepsTheOriginalPosition(t *testing.T) {
	r := NewRegistry()
	r.Register(standIn{id: "alpha"})
	r.Register(standIn{id: "mid"})
	r.Register(standIn{id: "zen"})

	replacement := standIn{id: "mid"}
	r.Register(replacement)

	if r.Len() != 3 {
		t.Fatalf("Len = %d after replacing one provider, want 3", r.Len())
	}
	got := r.IDs()
	if len(got) != 3 || got[0] != "alpha" || got[1] != "mid" || got[2] != "zen" {
		t.Fatalf("IDs = %v, the replaced provider changed position", got)
	}

	// Replace, not add: the registry must hand back the new provider.
	p, err := r.Get("mid")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p != Provider(replacement) {
		t.Fatalf("Get returned the previous provider after a replacement")
	}

	// All must agree with IDs: the gateway walks one and looks up by the other.
	all := r.All()
	if len(all) != len(got) {
		t.Fatalf("All has %d entries while IDs has %d", len(all), len(got))
	}
	for i, id := range got {
		if all[i].ID() != id {
			t.Fatalf("All[%d] is %q while IDs[%d] is %q", i, all[i].ID(), i, id)
		}
	}
}

// TestUnknownProviderIsAnError — an identifier that is not configured has to
// come back as an error, because the callers turn it into a route that does not
// exist rather than into an empty answer.
func TestUnknownProviderIsAnError(t *testing.T) {
	r := NewRegistry()
	r.Register(standIn{id: "alpha"})

	if p, err := r.Get("absent"); err == nil {
		t.Fatalf("Get on an unconfigured id returned %v and no error", p)
	}
}

// TestIDsIsACopy — the caller gets a copy of the order.
//
// A caller sorting or truncating the returned slice for a listing would
// otherwise reorder the registry itself, and the corruption would show up much
// later as providers appearing in the wrong place.
func TestIDsIsACopy(t *testing.T) {
	r := NewRegistry()
	r.Register(standIn{id: "alpha"})
	r.Register(standIn{id: "mid"})

	first := r.IDs()
	first[0] = "tampered"

	if got := r.IDs()[0]; got != "alpha" {
		t.Fatalf("the first identifier became %q after the caller wrote to a returned slice", got)
	}
}
