package proxy

import (
	"context"
	"net"
	"time"

	utls "github.com/refraction-networking/utls"
)

// Zen's free-tier gate responds 429 FreeUsageLimitError to TLS clients whose
// fingerprint does not match the real OpenCode client. This spec mirrors the
// ClientHello captured from opencode 1.19.4's embedded Bun/BoringSSL stack:
// BoringSSL cipher order, extension order, groups, signature algorithms, and
// no ALPN extension.
func bunHelloSpec() *utls.ClientHelloSpec {
	sigalgs := []utls.SignatureScheme{
		0x0904, 0x0905, 0x0906, 0x0804, 0x0403, 0x0807,
		0x0805, 0x0806, 0x0401, 0x0501, 0x0601, 0x0503,
		0x0603, 0x0201, 0x0203,
	}
	return &utls.ClientHelloSpec{
		CipherSuites: []uint16{
			0xc02b, // ECDHE-ECDSA-AES128-GCM-SHA256
			0xc02f, // ECDHE-RSA-AES128-GCM-SHA256
			0xc02c, // ECDHE-ECDSA-AES256-GCM-SHA384
			0xc030, // ECDHE-RSA-AES256-GCM-SHA384
			0xcca9, // ECDHE-ECDSA-CHACHA20-POLY1305
			0xcca8, // ECDHE-RSA-CHACHA20-POLY1305
			0xc009, // ECDHE-ECDSA-AES128-SHA
			0xc013, // ECDHE-RSA-AES128-SHA
			0xc00a, // ECDHE-ECDSA-AES256-SHA
			0xc014, // ECDHE-RSA-AES256-SHA
			utls.TLS_AES_128_GCM_SHA256,
			utls.TLS_AES_256_GCM_SHA384,
			utls.TLS_CHACHA20_POLY1305_SHA256,
		},
		CompressionMethods: []uint8{0},
		Extensions: []utls.TLSExtension{
			&utls.SNIExtension{},
			&utls.SupportedPointsExtension{SupportedPoints: []byte{0}},
			&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},
			&utls.ExtendedMasterSecretExtension{},
			&utls.SCTExtension{},
			&utls.StatusRequestExtension{},
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{
				utls.X25519MLKEM768,
				utls.X25519,
				utls.CurveP256,
				utls.CurveP384,
				utls.CurveP521,
			}},
			&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: sigalgs},
			&utls.SignatureAlgorithmsCertExtension{SupportedSignatureAlgorithms: sigalgs},
			&utls.SupportedVersionsExtension{Versions: []uint16{
				utls.VersionTLS13,
				utls.VersionTLS12,
			}},
			&utls.KeyShareExtension{KeyShares: []utls.KeyShare{
				{Group: utls.X25519MLKEM768},
				{Group: utls.X25519},
			}},
		},
		// No GREASE, ALPN, session ticket, PSK modes, or padding: the
		// captured OpenCode hello carries exactly 11 extensions.
	}
}

// dialOpenCodeTLS opens a TCP connection and performs a TLS handshake that
// presents OpenCode's Bun fingerprint. serverName may be overridden by
// tests; production callers leave it empty to use the dial address host.
func dialOpenCodeTLS(ctx context.Context, network, addr, serverName string) (net.Conn, error) {
	if network == "tcp" {
		network = "tcp4"
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	raw, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if serverName == "" {
		serverName, _, err = net.SplitHostPort(addr)
		if err != nil {
			serverName = addr
		}
	}
	config := &utls.Config{ServerName: serverName}
	conn := utls.UClient(raw, config, utls.HelloCustom)
	if err := conn.ApplyPreset(bunHelloSpec()); err != nil {
		raw.Close()
		return nil, err
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return conn, nil
}
