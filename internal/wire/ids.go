package wire

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"sync"
	"time"
)

// The identifier contract is derived from
// packages/schema/src/identifier.ts (opencode 2.0.30):
//
//	const length = 26
//	const chars  = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
//
//	export function create(descending, timestamp = Date.now()) {
//	  if (timestamp !== lastTimestamp) { lastTimestamp = timestamp; counter = 0 }
//	  counter++
//	  const current = BigInt(timestamp) * 0x1000n + BigInt(counter)
//	  const value   = descending ? ~current : current
//	  const time = 6 bytes of value, big-endian, as 12 hex characters
//	  const bytes = crypto.getRandomValues(new Uint8Array(length - 12))
//	  return time + the bytes array, each chars[byte % 62]
//	}
//
// Two things here are not cosmetics but part of the formula:
//
//   - value is inverted bitwise (descending), so identifiers DECREASE over
//     time;
//   - the counter resets when the millisecond changes, rather than behaving
//     like a process-wide global counter.
//
// SessionID additionally gets the "ses_" prefix (session-id.ts), a message id
// gets "msg_" (session-message.ts).
const (
	// idTotalLength is the total length of the part following the prefix:
	// 12 hex of time plus 14 base62.
	idTotalLength = 26

	// idTimeLength is the length of the time part in hex characters (6 bytes).
	idTimeLength = 12

	// base62Alphabet is the alphabet of the tail part, verbatim from identifier.ts.
	base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

	// idCounterBits is how many low bits of value the counter occupies
	// (BigInt(timestamp) * 0x1000n leaves 12 bits).
	idCounterBits = 12

	// idTimeMask is the mask of the 48 bits that land in the six-byte field.
	idTimeMask = 0xFFFFFFFFFFFF
)

// SessionIDPrefix is the prefix every session id carries.
const SessionIDPrefix = "ses_"

// MessageIDPrefix is the prefix every message id carries.
const MessageIDPrefix = "msg_"

// canonicalSessionPattern recognises an already canonical session id, so as not
// to rebuild it. It matches session-id.ts in shape: "ses_" + 12 hex + 14 base62.
var canonicalSessionPattern = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

// idState is the counter equivalent to the module-level lastTimestamp/counter
// from identifier.ts. Clients mint identifiers rarely, so a single mutex is
// enough and it faithfully reproduces the original semantics.
var idState struct {
	mu        sync.Mutex
	lastMilli int64
	counter   int64
}

// NewSessionID returns a new session id built by the client's formula.
func NewSessionID() string { return NewSessionIDAt(time.Now().UnixMilli()) }

// NewSessionIDAt is NewSessionID with an injected clock, so tests can pin the
// arithmetic without freezing time process-wide.
func NewSessionIDAt(nowMillis int64) string {
	return SessionIDPrefix + newIDAt(nowMillis)
}

// NewMessageID returns a new message id (the "msg_" prefix).
func NewMessageID() string { return NewMessageIDAt(time.Now().UnixMilli()) }

// NewMessageIDAt is NewMessageID with an injected clock.
func NewMessageIDAt(nowMillis int64) string {
	return MessageIDPrefix + newIDAt(nowMillis)
}

// CanonicalSessionID accepts an already canonical id unchanged, and otherwise
// mints a new one.
//
// A separate function is needed because the client may send a ready-made id: it
// must not be rebuilt, it is part of the session history.
func CanonicalSessionID(id string) string {
	if canonicalSessionPattern.MatchString(id) {
		return id
	}
	return NewSessionID()
}

// IsCanonicalSessionID reports whether id already has the admitted form.
func IsCanonicalSessionID(id string) bool { return canonicalSessionPattern.MatchString(id) }

// newIDAt is the deterministic core of the formula, returning the part without
// the prefix.
func newIDAt(nowMillis int64) string {
	counter := nextCounter(nowMillis)

	// current = timestamp * 4096 + counter, then the complement and a mask down
	// to the 48 bits the six-byte field carries.
	current := uint64(nowMillis)<<idCounterBits + uint64(counter)
	value := (^current) & idTimeMask

	timePart := hex.EncodeToString([]byte{
		byte(value >> 40), byte(value >> 32), byte(value >> 24),
		byte(value >> 16), byte(value >> 8), byte(value),
	})

	return timePart + randomBase62(idTotalLength-idTimeLength)
}

// nextCounter reproduces the counter reset when the millisecond changes.
func nextCounter(nowMillis int64) int64 {
	idState.mu.Lock()
	defer idState.mu.Unlock()
	if nowMillis != idState.lastMilli {
		idState.lastMilli = nowMillis
		idState.counter = 0
	}
	idState.counter++
	return idState.counter
}

// randomBase62 returns width characters of the alphabet, each chosen from a
// random byte via the remainder — verbatim as Array.from(bytes, byte => chars[byte % 62]).
//
// A remainder of 256 modulo 62 gives a slight bias (bytes 0..247 are
// distributed evenly, 248..255 give extra hits on the first 8 characters). For
// an identifier that is irrelevant: it is not obliged to be uniform, it is
// obliged to have the same shape as the client's.
func randomBase62(width int) string {
	buf := make([]byte, width)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand does not refuse in practice; we will not replace the
		// panic with a math/rand-like fallback — that would hide a serious
		// environment problem.
		panic(fmt.Sprintf("wire: crypto/rand unavailable: %v", err))
	}
	out := make([]byte, width)
	for i, b := range buf {
		out[i] = base62Alphabet[int(b)%len(base62Alphabet)]
	}
	return string(out)
}

// DecodeSessionTimestamp extracts the moment in time encoded in a session id.
//
// A NOTE ON PRECISION. The six-byte field stores 48 bits, while
// `timestamp * 4096` takes up 53 bits, so the top five bits of the value are
// lost on write. The client itself behaves the same way: identifier.ts shifts
// the BigInt right and takes a byte at a time, discarding whatever does not
// fit. So the recovered moment is the timestamp truncated to 36 bits, and it is
// NOT equal to the original for values above roughly 2^36 (about 68.7 billion
// ms, that is past 1972).
//
// The function is therefore good for diagnosing the shape and for tests, but
// not as an assertion "the request time is in here". The canonicity of an id is
// determined by its shape (§IsCanonicalSessionID), not by whether the time
// inside reads back.
func DecodeSessionTimestamp(id string) (time.Time, error) {
	at, _, err := DecodeSessionTimestampCheck(id)
	return at, err
}

// DecodeSessionTimestampCheck additionally returns the counter from the low
// bits — it is needed by the tests that check the counter reset when the
// millisecond changes.
func DecodeSessionTimestampCheck(id string) (time.Time, int64, error) {
	if !canonicalSessionPattern.MatchString(id) {
		return time.Time{}, 0, fmt.Errorf("wire: %q is not a canonical session id", id)
	}
	timePart := id[len(SessionIDPrefix) : len(SessionIDPrefix)+idTimeLength]
	raw, err := hex.DecodeString(timePart)
	if err != nil || len(raw) != 6 {
		return time.Time{}, 0, fmt.Errorf("wire: session id %q has an undecodable time part", id)
	}

	var value uint64
	for _, b := range raw {
		value = value<<8 | uint64(b)
	}
	current := ^value & idTimeMask
	millis := int64(current >> idCounterBits)
	counter := int64(current & (1<<idCounterBits - 1))
	return time.UnixMilli(millis), counter, nil
}

// base62Value is used by the tests only: it returns the index of a character in
// the alphabet, so that assertions can talk about the shape of the tail.
func base62Value(c byte) (int, bool) {
	for i := 0; i < len(base62Alphabet); i++ {
		if base62Alphabet[i] == c {
			return i, true
		}
	}
	return 0, false
}

// timeFieldOf reproduces exactly what identifier.ts does with the time field —
// including the loss of the top bits. It exists so that the reference value for
// the tests is produced by the same code the gateway uses, and a divergence
// from the client implementation is visible at once.
func timeFieldOf(millis, counter uint64) string {
	value := (^(millis<<idCounterBits + counter)) & idTimeMask
	return hex.EncodeToString([]byte{
		byte(value >> 40), byte(value >> 32), byte(value >> 24),
		byte(value >> 16), byte(value >> 8), byte(value),
	})
}

// bigIntFromBase62 assembles a number from the alphabet string — a helper for
// the tests, which need the order rather than the value.
func bigIntFromBase62(s string) *big.Int {
	out := new(big.Int)
	base := big.NewInt(int64(len(base62Alphabet)))
	for i := 0; i < len(s); i++ {
		v, ok := base62Value(s[i])
		if !ok {
			return nil
		}
		out.Mul(out, base)
		out.Add(out, big.NewInt(int64(v)))
	}
	return out
}
