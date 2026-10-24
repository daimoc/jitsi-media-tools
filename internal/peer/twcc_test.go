package peer

import (
	"testing"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/transport/v2/packetio"
)

// offerWithTWCC declares one remote audio source (SSRC 11) and the
// transport-cc header extension with ID 5.
const offerWithTWCC = "v=0\r\no=- 1 2 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\n" +
	"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\nc=IN IP4 0.0.0.0\r\na=mid:audio-1\r\na=sendonly\r\n" +
	"a=extmap:5 http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01\r\n" +
	"a=rtpmap:111 opus/48000/2\r\na=ssrc:11 cname:a\r\n"

// twccPacket returns an RTP packet of ssrc with transport sequence seq in
// extension id (none if id is 0).
func twccPacket(t *testing.T, ssrc uint32, seq uint16, id uint8) []byte {
	t.Helper()
	p := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SSRC: ssrc, SequenceNumber: seq}, Payload: []byte{1, 2, 3}}
	if id != 0 {
		ext, err := (&rtp.TransportCCExtension{TransportSequence: seq}).Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Header.SetExtension(id, ext); err != nil {
			t.Fatal(err)
		}
	}
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// acknowledged returns the number of packets reported received in fb.
func acknowledged(t *testing.T, fb []rtcp.Packet) int {
	t.Helper()
	n := 0
	for _, p := range fb {
		if cc, ok := p.(*rtcp.TransportLayerCC); ok {
			n += len(cc.RecvDeltas)
		}
	}
	return n
}

func TestTransportFeedback_AcknowledgesEveryArrivingPacket(t *testing.T) {
	p := newTestPeer(t)
	buffer := func(ssrc uint32) *gatedBuffer {
		return &gatedBuffer{peer: p, ssrc: ssrc, rtp: true, buf: packetio.NewBuffer()}
	}
	kept, dropped := buffer(11), buffer(99) // 99 is not in the offer

	// Before an offer with the extension: nothing to acknowledge.
	kept.Write(twccPacket(t, 11, 1, 5))
	if n := acknowledged(t, p.feedback.build()); n != 0 {
		t.Fatalf("expected no feedback before the offer, got %d packets", n)
	}

	p.recordOffered(offerWithTWCC)
	kept.Write(twccPacket(t, 11, 10, 5))
	dropped.Write(twccPacket(t, 99, 11, 5)) // Dropped by the gate, still acknowledged
	kept.Write(twccPacket(t, 11, 12, 5))
	kept.Write(twccPacket(t, 11, 13, 0)) // No transport-cc extension
	if n := acknowledged(t, p.feedback.build()); n != 3 {
		t.Errorf("expected 3 packets acknowledged (including the dropped one), got %d", n)
	}

	// RTCP buffers are not RTP: ignored.
	rtcpBuf := &gatedBuffer{peer: p, ssrc: 11, buf: packetio.NewBuffer()}
	rtcpBuf.Write(twccPacket(t, 11, 20, 5))
	if n := acknowledged(t, p.feedback.build()); n != 0 {
		t.Errorf("expected RTCP buffers ignored, got %d packets acknowledged", n)
	}
}
