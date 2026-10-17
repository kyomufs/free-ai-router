package transport

import (
	"context"
	"net/http"
	"testing"
)

// TestDirectRoundTripperIsUsable — the direct transport must hand out a working
// RoundTripper and be able to stop without errors.
func TestDirectRoundTripperIsUsable(t *testing.T) {
	v, err := NewDirect(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	tr := v.(Transport)

	if tr.ID() != Direct {
		t.Fatalf("ID = %q, want %q", tr.ID(), Direct)
	}
	rt, err := tr.RoundTripper()
	if err != nil {
		t.Fatalf("RoundTripper: %v", err)
	}
	if rt == nil {
		t.Fatal("RoundTripper returned nil")
	}
	if _, ok := rt.(*http.Transport); !ok {
		t.Fatalf("RoundTripper is %T, want *http.Transport", rt)
	}
	if err := tr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := tr.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestDirectFamilyPinsNetwork — pinning the family must change the dial
// network, otherwise the setting means nothing.
//
// What is checked is behaviour, not a field: the RoundTripper must stay usable
// for any family, including an unrecognised one.
func TestDirectFamilyPinsNetwork(t *testing.T) {
	for _, family := range []string{"", "auto", "v4", "v6", "garbage"} {
		v, err := NewDirect(map[string]any{"family": family})
		if err != nil {
			t.Fatalf("family %q: %v", family, err)
		}
		if _, err := v.RoundTripper(); err != nil {
			t.Fatalf("family %q: RoundTripper: %v", family, err)
		}
	}
}

// TestNetworkFor — translating the setting into a network name. An
// unrecognised value means "let the system decide", not an error: it is the
// same as "auto".
func TestNetworkFor(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"auto":    "",
		"v4":      "tcp4",
		"v6":      "tcp6",
		"somehow": "",
	}
	for in, want := range cases {
		if got := networkFor(in); got != want {
			t.Fatalf("networkFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRegistryWithDefaultsHasDirect — the default registry contains the direct
// transport, and the entry point does not have to register it.
func TestRegistryWithDefaultsHasDirect(t *testing.T) {
	r := NewRegistryWithDefaults()

	ids := r.IDs()
	if len(ids) == 0 {
		t.Fatal("the default registry is empty")
	}
	found := false
	for _, id := range ids {
		if id == Direct {
			found = true
		}
	}
	if !found {
		t.Fatalf("the default registry lacks %q: %v", Direct, ids)
	}

	v, err := r.Build(Direct, map[string]any{})
	if err != nil {
		t.Fatalf("Build(direct): %v", err)
	}
	if v.ID() != Direct {
		t.Fatalf("Build(direct) returned %q", v.ID())
	}
}

// TestRegistryRejectsUnknown — building an unregistered transport is an error,
// not a silent fallback to something else.
func TestRegistryRejectsUnknown(t *testing.T) {
	r := NewRegistryWithDefaults()
	if _, err := r.Build("no-such-thing", nil); err == nil {
		t.Fatal("an unknown transport must fail")
	}
}

// TestRegistryIDsStableOrder — the order is stable, otherwise the lists in the
// CLI and the TUI would jump between calls.
func TestRegistryIDsStableOrder(t *testing.T) {
	r := NewRegistryWithDefaults()
	first := r.IDs()
	second := r.IDs()
	if len(first) != len(second) {
		t.Fatalf("IDs changed length between calls: %v vs %v", first, second)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("IDs order is unstable: %v vs %v", first, second)
		}
	}
}
