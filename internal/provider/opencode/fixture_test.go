package opencode

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClassifyOnRecordedRefusal checks refusal parsing on a RECORDED service
// response rather than on an invented one.
//
// This is the only check of the refusal shape that cannot be derived from the
// code: the measurements in the plan were obtained by reading the sources, while
// here it is a live exchange. The file and its provenance are described in
// testdata/README.md.
//
// Careful: the response depends on the quota state at the moment of the request.
// If that changes, the test will show it — and then the FIXTURE is what needs
// updating, not the test being bent to fit.
func TestClassifyOnRecordedRefusal(t *testing.T) {
	body := readFixture(t, "refusal-freetier.json")

	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     make(http.Header),
	}
	resp.Header.Set("content-type", "application/json")

	failure := newTestProvider(t).Classify(resp, body)

	// The body must reach the client WITHOUT CHANGES: the client looks in it for
	// the error.type and error.code signals by which it decides whether to retry.
	if string(failure.Body) != string(body) {
		t.Fatalf("the refusal body was altered:\n got %s\nwant %s", failure.Body, body)
	}
	if failure.Status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", failure.Status)
	}

	// The recorded refusal is the FREE-LANE ATTESTATION one, and it is not about
	// the credential: no key makes a non-conforming request acceptable. Rotation
	// over it would only add refused requests from the same address, so the
	// decision must be "do not switch and do not repeat".
	if failure.KeyRejected {
		t.Fatal("the attestation refusal must not be blamed on the credential")
	}
	if failure.Retryable {
		t.Fatal("the attestation refusal must not be repeated")
	}
}

// TestAKeyRefusalIsStillBlamedOnTheKey — the read above is narrowed to the
// free-lane wording, not to 403 as a whole.
//
// A 403 that names a permission problem IS caused by the credentials, and there
// another key is worth trying: that is the branch the narrowing must not eat.
func TestAKeyRefusalIsStillBlamedOnTheKey(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"PermissionError","code":"permission_error","message":"This key is not allowed to use this model."}}`)
	resp := &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header)}

	failure := newTestProvider(t).Classify(resp, body)
	if !failure.KeyRejected {
		t.Fatal("a permission 403 must still be treated as a reason to try another key")
	}
}

// TestRecordedRefusalKeepsClientSignals checks that the signals by which the
// client makes its decision survive all the way to it.
//
// The signals are checked as substrings: the body is passed verbatim, and
// parsing it here would mean introducing a second parser of the same response.
func TestRecordedRefusalKeepsClientSignals(t *testing.T) {
	body := string(readFixture(t, "refusal-freetier.json"))

	for _, signal := range []string{`"type":"error"`, `"type":"FreeTierError"`, `"error"`} {
		if !strings.Contains(body, signal) {
			t.Fatalf("the recorded refusal lost the signal %s: %s", signal, body)
		}
	}
}

// TestClassifyLeavesAbsentHeaderAlone: the recorded refusal has no wait header,
// and the parsing must not invent one.
func TestClassifyLeavesAbsentHeaderAlone(t *testing.T) {
	body := readFixture(t, "refusal-freetier.json")
	resp := &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header)}

	failure := newTestProvider(t).Classify(resp, body)
	if failure.RetryAfter != 0 {
		t.Fatalf("RetryAfter = %v, want none: the service sent no retry-after", failure.RetryAfter)
	}
}

// readFixture reads a recorded exchange.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture %s: %v", path, err)
	}
	if len(raw) == 0 {
		t.Fatalf("fixture %s is empty", path)
	}
	return raw
}
