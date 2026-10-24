package peer

import (
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor/pkg/twcc"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
)

// transportFeedbackInterval is how often transport-wide congestion control
// feedback is sent (Pion's default).
const transportFeedbackInterval = 100 * time.Millisecond

// transportFeedback generates a peer's transport-wide congestion control
// feedback (TWCC) from every RTP packet that reaches its read buffers,
// including the packets of streams the peer drops (research R9 of 005).
// Pion's TWCC interceptor only sees the packets the application reads, so
// the media server would count the others, and its bandwidth probes, as
// lost and lower its estimate.
type transportFeedback struct {
	extID atomic.Uint32 // transport-cc header extension ID of the applied offer; 0: none
	start time.Time

	mu       sync.Mutex
	recorder *twcc.Recorder // Guarded by mu

	runOnce sync.Once
	done    chan struct{}
	stop    sync.Once
}

func newTransportFeedback() *transportFeedback {
	return &transportFeedback{
		start:    time.Now(),
		recorder: twcc.NewRecorder(rand.Uint32()),
		done:     make(chan struct{}),
	}
}

// setExtension records the transport-cc extension ID of an applied offer.
func (f *transportFeedback) setExtension(parsed *sdp.SessionDescription) {
	for _, md := range parsed.MediaDescriptions {
		for _, a := range md.Attributes {
			if a.Key != "extmap" || !strings.HasSuffix(a.Value, sdp.TransportCCURI) {
				continue
			}
			id, _, _ := strings.Cut(a.Value, " ")
			id, _, _ = strings.Cut(id, "/") // "5/sendrecv"
			if n, err := strconv.ParseUint(id, 10, 8); err == nil && n > 0 {
				f.extID.Store(uint32(n))
				return
			}
		}
	}
}

// record notes the arrival of an RTP packet.
func (f *transportFeedback) record(pkt []byte, arrival time.Time) {
	id := f.extID.Load()
	if id == 0 {
		return
	}
	var h rtp.Header
	if _, err := h.Unmarshal(pkt); err != nil {
		return
	}
	ext := h.GetExtension(uint8(id))
	if ext == nil {
		return
	}
	var tcc rtp.TransportCCExtension
	if err := tcc.Unmarshal(ext); err != nil {
		return
	}
	f.mu.Lock()
	f.recorder.Record(h.SSRC, tcc.TransportSequence, arrival.Sub(f.start).Microseconds())
	f.mu.Unlock()
}

// build returns the feedback for the packets recorded since the last call.
func (f *transportFeedback) build() []rtcp.Packet {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recorder.BuildFeedbackPacket()
}

// run starts sending the feedback with write, once, until close.
func (f *transportFeedback) run(write func([]rtcp.Packet) error) {
	f.runOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(transportFeedbackInterval)
			defer ticker.Stop()
			for {
				select {
				case <-f.done:
					return
				case <-ticker.C:
					if pkts := f.build(); len(pkts) > 0 {
						_ = write(pkts) // Fails until the transport is up
					}
				}
			}
		}()
	})
}

func (f *transportFeedback) close() {
	f.stop.Do(func() { close(f.done) })
}
