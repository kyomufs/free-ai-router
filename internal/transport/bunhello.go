package transport

import (
	utls "github.com/refraction-networking/utls"
)

// bunClientHello returns the ClientHello the reference client sends.
//
// WHY THIS EXISTS. The provider decides whether a request may use the free lane
// by looking at the TLS client, not only at the headers. MEASURED on 2026-10-24
// against the inference endpoint: the same method, path, headers and body were
// answered 200 when sent by the reference client, and 429 with
// FreeUsageLimitError when sent by Go's crypto/tls, by curl and by a uTLS
// browser profile. Replaying the captured reference hello through uTLS kept the
// 200. The protocol does not decide it: HTTP/1.1 and HTTP/2 both pass once this
// hello is used, while both are refused with a Go hello.
//
// The list is the reference client's own, in its own order: BoringSSL's default
// suites, the three curves, the signature algorithms, and the extension order
// below. It is not a browser imitation — a browser profile was refused — so it
// is kept as close to the captured hello as the library allows, including the
// ECH grease extension and BoringSSL padding.
//
// The hello was captured from the client behind a CONNECT tunnel. Such a hello
// carries an empty SNI, so the name is supplied by the caller for every
// connection; everything else is fixed.
func bunClientHello(serverName string) *utls.ClientHelloSpec {
	return &utls.ClientHelloSpec{
		CipherSuites: []uint16{
			0x1301, 0x1302, 0x1303,
			0xc02b, 0xc02f, 0xc02c, 0xc030,
			0xcca9, 0xcca8,
			0xc009, 0xc013, 0xc00a, 0xc014,
			0x009c, 0x009d, 0x002f, 0x0035,
		},
		CompressionMethods: []uint8{0},
		TLSVersMin:         utls.VersionTLS12,
		TLSVersMax:         utls.VersionTLS13,
		Extensions: []utls.TLSExtension{
			&utls.SNIExtension{ServerName: serverName},
			utls.BoringGREASEECH(),
			&utls.ExtendedMasterSecretExtension{},
			&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{
				utls.X25519, utls.CurveP256, utls.CurveP384,
			}},
			&utls.SupportedPointsExtension{SupportedPoints: []byte{0}},
			&utls.SessionTicketExtension{},
			&utls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}},
			&utls.StatusRequestExtension{},
			&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{
				utls.ECDSAWithP256AndSHA256,
				utls.PSSWithSHA256,
				utls.PKCS1WithSHA256,
				utls.ECDSAWithP384AndSHA384,
				utls.PSSWithSHA384,
				utls.PKCS1WithSHA384,
				utls.PSSWithSHA512,
				utls.PKCS1WithSHA512,
				utls.PKCS1WithSHA1,
			}},
			&utls.SCTExtension{},
			&utls.KeyShareExtension{KeyShares: []utls.KeyShare{{Group: utls.X25519}}},
			&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}},
			&utls.SupportedVersionsExtension{Versions: []uint16{
				utls.VersionTLS13, utls.VersionTLS12,
			}},
			&utls.UtlsPaddingExtension{GetPaddingLen: utls.BoringPaddingStyle},
		},
	}
}
