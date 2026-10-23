package opencode

import (
	"net/http"
	"testing"
)

// The pool's promotion criterion is "the provider refused this source address", so
// the classifier must report it as its own judgement rather than fold it into the
// key one.
func TestClassifyMarksAddressRefusal(t *testing.T) {
	p := &Provider{}
	bodies := []string{
		`{"error":{"message":"Free models aren't available from your current network"}}`,
		`{"error":{"message":"We have detected abusive traffic patterns from your network"}}`,
	}
	for _, body := range bodies {
		resp := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}
		f := p.Classify(resp, []byte(body))
		if !f.AddressRejected {
			t.Fatalf("address refusal not marked for %s", body)
		}
		if f.KeyRejected {
			t.Fatalf("an address refusal must not look like a key refusal: %s", body)
		}
	}

	// A quota refusal is about the account, not the address: rotating addresses
	// would spend the pool on nothing.
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}
	f := p.Classify(resp, []byte(`{"error":{"code":"insufficient_quota"}}`))
	if f.AddressRejected {
		t.Fatal("a quota refusal must not be reported as an address refusal")
	}
}
