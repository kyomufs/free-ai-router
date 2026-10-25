package transport

import (
	"testing"

	utls "github.com/refraction-networking/utls"
)

// TestBunClientHelloShape — the hello is a measured contract, not a preference.
//
// The values are checked because the service refuses a Go hello with the same
// request, and any edit here silently changes what the service sees. The test
// does not contact the service: it only keeps the shape that was measured to be
// accepted.
func TestBunClientHelloShape(t *testing.T) {
	spec := bunClientHello("example.test")

	if len(spec.CipherSuites) != 17 {
		t.Fatalf("cipher suites = %d, want 17", len(spec.CipherSuites))
	}
	if spec.CipherSuites[0] != utls.TLS_AES_128_GCM_SHA256 {
		t.Fatalf("first cipher = %#x, want TLS_AES_128_GCM_SHA256", spec.CipherSuites[0])
	}
	if len(spec.Extensions) != 15 {
		t.Fatalf("extensions = %d, want 15", len(spec.Extensions))
	}

	sni, ok := spec.Extensions[0].(*utls.SNIExtension)
	if !ok || sni.ServerName != "example.test" {
		t.Fatalf("first extension = %#v, want SNI example.test", spec.Extensions[0])
	}

	var alpn []string
	var curves []utls.CurveID
	for _, ext := range spec.Extensions {
		switch e := ext.(type) {
		case *utls.ALPNExtension:
			alpn = e.AlpnProtocols
		case *utls.SupportedCurvesExtension:
			curves = e.Curves
		}
	}
	if len(alpn) != 1 || alpn[0] != "http/1.1" {
		t.Fatalf("ALPN = %v, want [http/1.1]", alpn)
	}
	wantCurves := []utls.CurveID{utls.X25519, utls.CurveP256, utls.CurveP384}
	if len(curves) != len(wantCurves) {
		t.Fatalf("curves = %v, want %v", curves, wantCurves)
	}
	for i := range wantCurves {
		if curves[i] != wantCurves[i] {
			t.Fatalf("curves = %v, want %v", curves, wantCurves)
		}
	}
}
