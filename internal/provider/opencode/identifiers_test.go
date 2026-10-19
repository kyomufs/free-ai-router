package opencode

import (
	"strings"
	"testing"
)

// TestSessionIDShape checks the shape of the identifier: the prefix, 12 hex of
// time and 14 tail characters. This is the first thing the gate sees, and the
// shape is derived from identifier.ts.
func TestSessionIDShape(t *testing.T) {
	for i := 0; i < 50; i++ {
		id := newSessionID()

		if !strings.HasPrefix(id, sessionIDPrefix) {
			t.Fatalf("id %q lacks the %q prefix", id, sessionIDPrefix)
		}
		body := id[len(sessionIDPrefix):]
		if len(body) != idTotalLength {
			t.Fatalf("id %q has body length %d, want %d", id, len(body), idTotalLength)
		}
		if got := body[:idTimeLength]; !isLowerHex(got) {
			t.Fatalf("id %q: time part %q is not lowercase hex", id, got)
		}
		for _, c := range body[idTimeLength:] {
			if _, ok := base62Value(byte(c)); !ok {
				t.Fatalf("id %q: tail char %q is outside the base62 alphabet", id, c)
			}
		}
		if !isSessionID(id) {
			t.Fatalf("id %q is not recognised as canonical", id)
		}
	}
}

func TestMessageIDShape(t *testing.T) {
	id := newMessageID()
	if !strings.HasPrefix(id, messageIDPrefix) {
		t.Fatalf("id %q lacks the %q prefix", id, messageIDPrefix)
	}
	if body := id[len(messageIDPrefix):]; len(body) != idTotalLength {
		t.Fatalf("id %q has body length %d, want %d", id, len(body), idTotalLength)
	}
}

// TestTimestampRoundTripWithinField — the main property under test of the
// formula: as long as the timestamp fits in 36 bits, encoding and decoding are
// mutual inverses.
func TestTimestampRoundTripWithinField(t *testing.T) {
	const hi = uint64(idTimeMask >> idCounterBits) // 2^36 - 1

	for _, ts := range []uint64{0, 1, 1000, 1 << 20, 1 << 30, hi - 1, hi} {
		for _, counter := range []uint64{1, 5, 4095} {
			id := sessionIDPrefix + timeFieldOf(ts, counter) + strings.Repeat("0", 14)
			at, err := decodeSessionTimestamp(id)
			if err != nil {
				t.Fatalf("ts=%d: %v", ts, err)
			}
			if uint64(at.UnixMilli()) != ts {
				t.Fatalf("ts=%d counter=%d: decoded %d, want %d", ts, counter, at.UnixMilli(), ts)
			}
		}
	}
}

// TestTimestampAboveFieldIsTruncated pins down the BOUNDARY of the property: a
// real-time timestamp (41 bits) does not fit in the six-byte field, the top bits
// are discarded on write, and invertibility is lost.
//
// This is not a defect of this code but a property of the client's format.
// The test is written so that the boundary is not taken for a bug and the
// formula is not "fixed" in the convenient direction: the shape must match the
// client's.
func TestTimestampAboveFieldIsTruncated(t *testing.T) {
	const hi = uint64(idTimeMask >> idCounterBits)

	// Checking exactly at the boundary: hi+1 no longer fits.
	id := sessionIDPrefix + timeFieldOf(hi+1, 1) + strings.Repeat("0", 14)
	at, err := decodeSessionTimestamp(id)
	if err != nil {
		t.Fatal(err)
	}
	if uint64(at.UnixMilli()) == hi+1 {
		t.Fatalf("expected truncation above the field width, got an exact round-trip")
	}
	if at.UnixMilli() != 0 {
		t.Fatalf("hi+1 truncates to 0, got %d", at.UnixMilli())
	}
}

// TestLiveTimestampIsRepresentable — the practical half: today's timestamp is
// truncated, but the identifier stays canonical, and that is all that is
// required to get through.
func TestLiveTimestampIsRepresentable(t *testing.T) {
	const live = 1_792_000_000_000

	id := newSessionIDAt(live)
	if !isSessionID(id) {
		t.Fatalf("a live timestamp produced non-canonical id %q", id)
	}
	if _, err := decodeSessionTimestamp(id); err != nil {
		t.Fatalf("live id %q does not decode: %v", id, err)
	}
}

// TestIDsDescend checks the consequence of the complement: for a later moment
// the time field is SMALLER. That is the point of descending() — without the
// inversion the formula would be just a hash.
func TestIDsDescend(t *testing.T) {
	const hi = uint64(idTimeMask >> idCounterBits)

	// hi-1000 is a later moment than hi-2000, so its field is smaller.
	earlier := timeFieldOf(hi-2000, 1)
	later := timeFieldOf(hi-1000, 1)

	if later >= earlier {
		t.Fatalf("expected the later timestamp to sort lower: earlier=%s later=%s", earlier, later)
	}

	// And the same on real identifiers.
	const base int64 = 1_700_000_000_000
	first := newSessionIDAt(base)
	second := newSessionIDAt(base + 1)
	field := func(id string) string { return id[len(sessionIDPrefix) : len(sessionIDPrefix)+idTimeLength] }
	if field(second) >= field(first) {
		t.Fatalf("later id must sort lower: first=%s second=%s", field(first), field(second))
	}
}

// TestCounterResetsPerMillisecond reproduces the counter reset: two identifiers
// in the same millisecond differ by the counter, while in different
// milliseconds they start over from one.
func TestCounterResetsPerMillisecond(t *testing.T) {
	const milli int64 = 1_750_000_000_000

	_, c1, err := decodeSessionTimestampCheck(newSessionIDAt(milli))
	if err != nil {
		t.Fatal(err)
	}
	_, c2, err := decodeSessionTimestampCheck(newSessionIDAt(milli))
	if err != nil {
		t.Fatal(err)
	}
	if c2 != c1+1 {
		t.Fatalf("same-millisecond counter: got %d after %d, want an increment", c2, c1)
	}
	_, c3, err := decodeSessionTimestampCheck(newSessionIDAt(milli + 1))
	if err != nil {
		t.Fatal(err)
	}
	if c3 != 1 {
		t.Fatalf("counter after a millisecond change: got %d, want 1 (reset)", c3)
	}
}

// TestCanonicalSessionIDKeepsValidID: a ready-made canonical id is not rebuilt.
// It is part of the session history, and substituting it would break that.
func TestCanonicalSessionIDKeepsValidID(t *testing.T) {
	original := newSessionID()
	if got := sessionID(original); got != original {
		t.Fatalf("a canonical id must pass through: got %q, want %q", got, original)
	}
}

// TestCanonicalSessionIDReplacesInvalid: a non-canonical id is replaced with a
// fresh one.
func TestCanonicalSessionIDReplacesInvalid(t *testing.T) {
	cases := []string{
		"",
		"ses_short",
		"ses_" + strings.Repeat("Z", 26),
		strings.Repeat("a", 26),
		"ses_" + strings.Repeat("a", 25),
		"ses_" + strings.Repeat("a", 27),
		"msg_" + strings.Repeat("a", 26),
	}
	for _, in := range cases {
		got := sessionID(in)
		if !isSessionID(got) {
			t.Fatalf("input %q produced non-canonical %q", in, got)
		}
		if got == in {
			t.Fatalf("input %q was passed through but is not canonical", in)
		}
	}
}

// TestDecodeRejectsForeignID: a foreign string is not decoded silently.
func TestDecodeRejectsForeignID(t *testing.T) {
	for _, in := range []string{"", "ses_x", "msg_" + strings.Repeat("a", 26), "random"} {
		if _, err := decodeSessionTimestamp(in); err == nil {
			t.Fatalf("input %q decoded without error", in)
		}
	}
}

// TestCounterBitsBound: the counter does not exceed 12 bits, otherwise it would
// overwrite the time field.
func TestCounterBitsBound(t *testing.T) {
	const milli int64 = 1_760_000_000_000
	for i := 0; i < (1<<idCounterBits)+5; i++ {
		_, counter, err := decodeSessionTimestampCheck(newSessionIDAt(milli))
		if err != nil {
			t.Fatal(err)
		}
		if counter < 0 || counter >= 1<<idCounterBits {
			t.Fatalf("counter %d is outside the %d-bit field", counter, idCounterBits)
		}
	}
}

func isLowerHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
