package capability

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// Retry — the mechanism that repeats a request the provider asked to repeat.
//
// WHY THIS IS A SECOND FAMILY, and not a setting of key rotation. The two answer
// different refusals. Key rotation answers "these credentials are spent" and
// changes WHO is asking. Retry answers "not now" — the same credentials, the same
// request, sent again after the delay the provider named — and it changes WHEN.
// A provider that limits the address rather than the key cannot be helped by
// another key at all; it can only be helped by waiting.
//
// WHAT IT DELIBERATELY DOES NOT DO. It does not decide what is retryable. That
// judgement belongs to the provider, which is the only party able to read its own
// refusals, and it arrives here already made. Nor is this the client's retry
// policy: the client has one of its own, and it must not be taken away — what
// happens here is invisible to it except by latency.
//
// IT IS A POLICY, NOT A COUNTER. The number of attempts is per REQUEST, so it
// cannot live here: one shared counter would let the first request of the day
// spend the whole budget and leave every later one with none. The gateway loop
// keeps its own count and asks this type only for the numbers.
type Retry struct {
	mu sync.Mutex

	// attempts — how many times one request may be sent in total. One means "no
	// repeat": the first answer is the answer.
	attempts int

	// delay — how long to wait before repeating when the provider named nothing.
	delay time.Duration

	// maxDelay — the ceiling on a delay the provider named. Providers answer with
	// an hour on occasion, and honouring that would hold a client connection open
	// for an hour to no purpose.
	maxDelay time.Duration

	// jitter — the share of the delay that is randomised. Without it, several
	// requests refused at the same moment come back at the same moment, and the
	// limit is hit again by the crowd they form.
	jitter float64

	// rnd — the source of the jitter. Its own, not the global one: the global
	// generator is shared with everything else in the process, and the lock on it
	// would become a point of contention on every retry.
	rnd *rand.Rand
}

// RetryConfig — the mechanism's settings, parsed from the configuration.
type RetryConfig struct {
	// Attempts — the total number of sends of one request. Absent means one,
	// which is to say the mechanism does nothing.
	Attempts int
	// DelayMs — the wait before a repeat, in milliseconds.
	DelayMs int
	// MaxDelayMs — the ceiling on a delay the provider named.
	MaxDelayMs int
	// Jitter — the randomised share of the delay, from 0 to 1.
	Jitter float64
}

// Defaults for the settings that were not named.
const (
	DefaultRetryDelayMs    = 500
	DefaultRetryMaxDelayMs = 10000
	DefaultRetryJitter     = 0.1
)

// NewRetry creates the mechanism from the configuration.
//
// No setting is mandatory: a mechanism named in the configuration but left empty
// still does the sensible thing, because "retry" is a decision to repeat and the
// numbers are detail.
func NewRetry(cfg map[string]any) (any, error) {
	parsed := RetryConfig{
		DelayMs:    DefaultRetryDelayMs,
		MaxDelayMs: DefaultRetryMaxDelayMs,
		Jitter:     DefaultRetryJitter,
	}

	for _, field := range []struct {
		name string
		dst  *int
	}{
		{"attempts", &parsed.Attempts},
		{"delay_ms", &parsed.DelayMs},
		{"max_delay_ms", &parsed.MaxDelayMs},
	} {
		raw, ok := cfg[field.name]
		if !ok {
			continue
		}
		v, err := retryNumber(field.name, raw)
		if err != nil {
			return nil, err
		}
		*field.dst = v
	}

	if raw, ok := cfg["jitter"]; ok {
		v, err := retryFloat("jitter", raw)
		if err != nil {
			return nil, err
		}
		parsed.Jitter = v
	}

	if parsed.Attempts < 1 {
		parsed.Attempts = 1
	}
	if parsed.DelayMs < 0 {
		parsed.DelayMs = 0
	}
	if parsed.MaxDelayMs <= 0 {
		parsed.MaxDelayMs = DefaultRetryMaxDelayMs
	}
	if parsed.Jitter < 0 {
		parsed.Jitter = 0
	}
	if parsed.Jitter > 1 {
		parsed.Jitter = 1
	}

	return &Retry{
		attempts: parsed.Attempts,
		delay:    time.Duration(parsed.DelayMs) * time.Millisecond,
		maxDelay: time.Duration(parsed.MaxDelayMs) * time.Millisecond,
		jitter:   parsed.Jitter,
		rnd:      rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

// retryNumber reads a number that may arrive as a JSON float.
func retryNumber(field string, raw any) (int, error) {
	switch v := raw.(type) {
	case int:
		return v, nil
	case float64:
		return int(v), nil
	default:
		return 0, fmt.Errorf("retry: %s is %T, want a number", field, raw)
	}
}

// retryFloat is retryNumber for a value that is legitimately fractional.
func retryFloat(field string, raw any) (float64, error) {
	switch v := raw.(type) {
	case int:
		return float64(v), nil
	case float64:
		return v, nil
	default:
		return 0, fmt.Errorf("retry: %s is %T, want a number", field, raw)
	}
}

// Attempts returns the total number of sends the mechanism permits for one
// request.
func (r *Retry) Attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}

// Delay returns the wait before a repeat.
//
// suggested is the delay the provider named; zero means it named none. A
// suggestion longer than the ceiling is cut down to it, and the jitter is applied
// on top.
func (r *Retry) Delay(suggested time.Duration) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	d := suggested
	if d <= 0 {
		d = r.delay
	}
	if d > r.maxDelay {
		d = r.maxDelay
	}
	if r.jitter <= 0 || d <= 0 {
		return d
	}

	// The jitter is symmetric: the wait may come out shorter as well as longer.
	// Shifting it in one direction only would push the average wait away from
	// the one that was configured.
	spread := float64(d) * r.jitter
	shift := (r.rnd.Float64()*2 - 1) * spread
	return time.Duration(float64(d) + shift)
}

// Info describes the mechanism for the help text and the interfaces.
func (r *Retry) Info() Info {
	return Info{
		Kind:    KindRetry,
		Name:    "retry",
		Summary: "repeats a request the provider asked to repeat, after its delay",
	}
}
