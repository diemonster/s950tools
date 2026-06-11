package device

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bivers/s950/internal/protocol"
)

// pingTransport implements transport.Transport with scriptable
// reply behavior — used to verify Device.Ping fires the right
// request and surfaces the timeout vs. happy-path distinction.
//
// Distinct from fakeSendTransport (putsample_test.go) which always
// returns (nil, nil) from RecvSysEx; that shape would mask the
// timeout error we care about here.
type pingTransport struct {
	sent          [][]byte
	reply         []byte // returned by RecvSysEx; nil = simulate timeout
	recvSysExErr  error
}

func (p *pingTransport) Send(b []byte) error {
	cp := make([]byte, len(b))
	copy(cp, b)
	p.sent = append(p.sent, cp)
	return nil
}
func (p *pingTransport) RecvSysEx(_ time.Duration) ([]byte, error) {
	if p.reply == nil {
		// Mimic the production transport's timeout error so the
		// caller's error-wrapping (`no reply within %v`) sees the
		// shape it expects.
		if p.recvSysExErr != nil {
			return nil, p.recvSysExErr
		}
		return nil, errors.New("simulated timeout waiting for SysEx")
	}
	return p.reply, nil
}
func (p *pingTransport) Drain()           {}
func (p *pingTransport) Close() error     { return nil }
func (p *pingTransport) InName() string   { return "fake" }
func (p *pingTransport) OutName() string  { return "fake" }

// Device.Ping is the pre-flight responsiveness check that guards
// long-running operations (sample upload) from silently no-op'ing
// when the device isn't listening. Hardware-discovered: cable
// plugged in serial, controller-select on MIDI → bytes vanished,
// no NAK came back, dialog reported success, upload never landed.

func TestPing_HappyPath_SendsRCATAndAcceptsAkaiReply(t *testing.T) {
	// Only an AKAI exclusive (F0 47 …) counts as the pong — see
	// Device.Ping's comment for the hardware crash that disproved
	// the old "any SysEx counts" contract.
	fake := &pingTransport{reply: []byte{0xF0, 0x47, 0x00, 0x0B, 0x40, 0x00, 0x00, 0xF7}}
	d := New(fake, 0)
	if err := d.Ping(500 * time.Millisecond); err != nil {
		t.Fatalf("Ping returned error on responsive transport: %v", err)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("Ping should send exactly one message; got %d", len(fake.sent))
	}
	// The request should be an RCAT: F0 47 chan FuncRCAT 40 00 00 F7.
	got := fake.sent[0]
	if len(got) < 8 || got[0] != protocol.SOX || got[3] != protocol.FuncRCAT {
		t.Errorf("Ping sent % X, want RCAT-shaped request", got)
	}
}

// ackThenCatalogTransport simulates the post-dump reality: the
// device is still streaming per-block ACK handshakes; the catalog
// reply only arrives once it has finished its bookkeeping.
type ackThenCatalogTransport struct {
	pingTransport
	acksLeft int
}

func (p *ackThenCatalogTransport) RecvSysEx(_ time.Duration) ([]byte, error) {
	if p.acksLeft > 0 {
		p.acksLeft--
		return []byte{0xF0, 0x7E, 0x7F, 0xF7}, nil // SDS ACK
	}
	return []byte{0xF0, 0x47, 0x00, 0x0B, 0x40, 0x00, 0x00, 0xF7}, nil
}

func TestPing_IgnoresHandshakeNoiseUntilAkaiReply(t *testing.T) {
	// The exact crash scenario from the 2026-06-10 wire log: a
	// stale ACK from the previous dump must NOT satisfy the next
	// upload's ping — Ping keeps listening until a real AKAI reply,
	// making it a device-ready barrier between uploads.
	fake := &ackThenCatalogTransport{acksLeft: 5}
	d := New(fake, 0)
	if err := d.Ping(500 * time.Millisecond); err != nil {
		t.Fatalf("Ping should skip ACK noise and accept the catalog: %v", err)
	}
	if fake.acksLeft != 0 {
		t.Errorf("Ping accepted a reply before draining the ACK noise (%d left)", fake.acksLeft)
	}
}

func TestPing_TimesOutOnPureHandshakeNoise(t *testing.T) {
	// A device that only ever streams ACKs (still mid-dump-
	// bookkeeping, never answering RCAT) must time out, not pass.
	fake := &ackThenCatalogTransport{acksLeft: 1 << 30}
	d := New(fake, 0)
	if err := d.Ping(150 * time.Millisecond); err == nil {
		t.Fatal("Ping must not succeed on handshake noise alone")
	}
}

func TestPing_TimesOutWhenNoReply(t *testing.T) {
	// The whole point of the test: if the device isn't listening,
	// Ping MUST return an error within ~timeout. A regression that
	// silently accepts timeouts would let SendSample run a full
	// no-op upload — exactly the hardware bug this guards against.
	fake := &pingTransport{reply: nil} // simulated timeout
	d := New(fake, 0)
	err := d.Ping(50 * time.Millisecond)
	if err == nil {
		t.Fatal("Ping should error on unresponsive transport")
	}
	if !strings.Contains(err.Error(), "no reply") {
		t.Errorf("Ping error should mention 'no reply'; got %v", err)
	}
}

func TestPing_PropagatesUnderlyingRecvError(t *testing.T) {
	// Distinct from a timeout: if the transport itself errored
	// (e.g. port closed), Ping should still report unreachability.
	fake := &pingTransport{recvSysExErr: errors.New("port closed")}
	d := New(fake, 0)
	if err := d.Ping(50 * time.Millisecond); err == nil {
		t.Fatal("expected error when transport recv fails")
	}
}

// SendSample uses Ping internally, but we test that wiring via the
// app-level test (cmd/s950-gui/sendsample_test.go would be the
// natural home). At the device layer, validating Ping itself is
// the most we can do without simulating the full Wails surface.
