package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

type capturedHello struct {
	ciphers []uint16
	extIDs  []uint16
	groups  []uint16
	sigalgs []uint16
	alpns   []string
}

func TestBunHelloMatchesCapturedFingerprint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	helloCh := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			close(helloCh)
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 8192)
		n, _ := io.ReadAtLeast(conn, buf, 5)
		helloCh <- buf[:n]
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		_, _ = dialOpenCodeTLS(ctx, "tcp", listener.Addr().String(), "opencode.ai")
	}()

	raw, ok := <-helloCh
	if !ok || len(raw) < 5 {
		t.Fatal("no ClientHello captured")
	}
	hello, err := parseCapturedHello(raw)
	if err != nil {
		t.Fatalf("parsing ClientHello: %v", err)
	}

	wantCiphers := []uint16{0xc02b, 0xc02f, 0xc02c, 0xc030, 0xcca9, 0xcca8, 0xc009, 0xc013, 0xc00a, 0xc014, 0x1301, 0x1302, 0x1303}
	if !equalU16(hello.ciphers, wantCiphers) {
		t.Fatalf("ciphers = %#x, want %#x", hello.ciphers, wantCiphers)
	}
	wantExts := []uint16{0, 11, 65281, 23, 18, 5, 10, 13, 50, 43, 51}
	if !equalU16(hello.extIDs, wantExts) {
		t.Fatalf("extensions = %v, want %v", hello.extIDs, wantExts)
	}
	wantGroups := []uint16{0x11ec, 0x001d, 0x0017, 0x0018, 0x0019}
	if !equalU16(hello.groups, wantGroups) {
		t.Fatalf("groups = %#x, want %#x", hello.groups, wantGroups)
	}
	wantSigalgs := []uint16{0x0904, 0x0905, 0x0906, 0x0804, 0x0403, 0x0807, 0x0805, 0x0806, 0x0401, 0x0501, 0x0601, 0x0503, 0x0603, 0x0201, 0x0203}
	if !equalU16(hello.sigalgs, wantSigalgs) {
		t.Fatalf("sigalgs = %#x, want %#x", hello.sigalgs, wantSigalgs)
	}
	if len(hello.alpns) != 0 {
		t.Fatalf("alpn = %v, want none", hello.alpns)
	}
}

func equalU16(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func parseCapturedHello(raw []byte) (*capturedHello, error) {
	if raw[0] != 0x16 {
		return nil, errNotHandshake
	}
	recordLen := int(binary.BigEndian.Uint16(raw[3:5]))
	hs := raw[5 : 5+recordLen]
	if hs[0] != 0x01 {
		return nil, errNotClientHello
	}
	body := hs[4:]
	pos := 2 + 32
	if pos >= len(body) {
		return nil, errShortHello
	}
	sessionLen := int(body[pos])
	pos += 1 + sessionLen
	cipherLen := int(binary.BigEndian.Uint16(body[pos : pos+2]))
	pos += 2
	hello := &capturedHello{}
	for i := 0; i < cipherLen; i += 2 {
		hello.ciphers = append(hello.ciphers, binary.BigEndian.Uint16(body[pos+i:pos+i+2]))
	}
	pos += cipherLen
	compLen := int(body[pos])
	pos += 1 + compLen
	extLen := int(binary.BigEndian.Uint16(body[pos : pos+2]))
	pos += 2
	end := pos + extLen
	for pos+4 <= end && pos+4 <= len(body) {
		extType := binary.BigEndian.Uint16(body[pos : pos+2])
		extDataLen := int(binary.BigEndian.Uint16(body[pos+2 : pos+4]))
		pos += 4
		data := body[pos : pos+extDataLen]
		pos += extDataLen
		hello.extIDs = append(hello.extIDs, extType)
		switch extType {
		case 10:
			n := int(binary.BigEndian.Uint16(data[0:2]))
			for i := 0; i < n; i += 2 {
				hello.groups = append(hello.groups, binary.BigEndian.Uint16(data[2+i:4+i]))
			}
		case 13:
			n := int(binary.BigEndian.Uint16(data[0:2]))
			for i := 0; i < n; i += 2 {
				hello.sigalgs = append(hello.sigalgs, binary.BigEndian.Uint16(data[2+i:4+i]))
			}
		case 16:
			offset := 2
			for offset < len(data) {
				l := int(data[offset])
				offset++
				if offset+l > len(data) {
					break
				}
				hello.alpns = append(hello.alpns, string(data[offset:offset+l]))
				offset += l
			}
		}
	}
	return hello, nil
}

type helloParseError string

func (e helloParseError) Error() string { return string(e) }

const (
	errNotHandshake   = helloParseError("not a TLS handshake record")
	errNotClientHello = helloParseError("not a ClientHello")
	errShortHello     = helloParseError("truncated ClientHello")
)
