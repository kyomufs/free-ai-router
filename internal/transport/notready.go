package transport

import "errors"

// ErrNotReady is returned by a transport that has nothing to send through yet.
//
// It exists as a shared value because the answer differs from every other
// transport failure: no provider was unreachable and nothing refused the request —
// there is simply no path at all. The gateway has to say that instead of blaming
// the provider, and it can only do so if the transport names the condition.
var ErrNotReady = errors.New("transport is not ready")
