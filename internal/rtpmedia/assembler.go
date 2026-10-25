package rtpmedia

import "github.com/pion/rtp"

// assembler rebuilds video frames from RTP packets. Unlike Pion's sample
// builder, which waits for the next frame's first packet, it releases a
// frame at its marker packet: one frame interval sooner. A frame whose
// marker never comes (the bridge dropped its last packets) ends when the
// next frame starts.
type assembler struct {
	d rtp.Depacketizer
	// maxLate bounds the wait for a missing packet: in packets buffered, and
	// in RTP time between the incomplete frame and the newest packet.
	maxLate   int
	maxLateTS uint32

	started bool
	next    uint16 // Sequence number of the next packet to use
	buf     map[uint16]*rtp.Packet
	newest  uint32 // Newest timestamp buffered
	dropped int    // Packets lost since the last frame out
}

// frame is an assembled frame; dropped counts the packets lost before it.
type frame struct {
	data      []byte
	timestamp uint32
	dropped   int
}

func newAssembler(d rtp.Depacketizer, maxLate int, maxLateTS uint32) *assembler {
	return &assembler{d: d, maxLate: maxLate, maxLateTS: maxLateTS, buf: map[uint16]*rtp.Packet{}}
}

// push adds a packet and returns the frames it completes, in order.
func (a *assembler) push(p *rtp.Packet) []frame {
	if !a.started {
		if !a.d.IsPartitionHead(p.Payload) {
			a.dropped++
			return nil // Wait for a frame start
		}
		a.started, a.next, a.newest = true, p.SequenceNumber, p.Timestamp
	}
	if int16(p.SequenceNumber-a.next) < 0 {
		return nil // Late or duplicate: its frame is gone
	}
	a.buf[p.SequenceNumber] = p
	if int32(p.Timestamp-a.newest) > 0 {
		a.newest = p.Timestamp
	}
	var out []frame
	for {
		if f, ok := a.take(); ok {
			if f.data != nil {
				out = append(out, f)
			}
			continue
		}
		if !a.giveUp() {
			return out
		}
	}
}

// take removes the frame starting at next when it is complete: contiguous
// packets up to a marker, or up to a packet of another timestamp. A frame
// that does not start with a frame start is dropped (nil data).
func (a *assembler) take() (frame, bool) {
	first := a.buf[a.next]
	if first == nil {
		return frame{}, false
	}
	end, n := a.next, 0
	for {
		p := a.buf[end]
		if p == nil {
			return frame{}, false // Gap, or the frame goes on
		}
		if p.Timestamp != first.Timestamp {
			break
		}
		end++
		n++
		if p.Marker {
			break
		}
	}
	f := frame{timestamp: first.Timestamp}
	head := a.d.IsPartitionHead(first.Payload)
	for s := a.next; s != end; s++ {
		if head {
			b, err := a.d.Unmarshal(a.buf[s].Payload)
			if err != nil {
				head = false
			}
			f.data = append(f.data, b...)
		}
		delete(a.buf, s)
	}
	a.next = end
	if !head {
		a.dropped += n
		return frame{}, true
	}
	if f.data == nil {
		f.data = []byte{}
	}
	f.dropped, a.dropped = a.dropped, 0
	return f, true
}

// giveUp drops the incomplete frame at next once waiting is over, moving
// next to the following frame start; false when it keeps waiting.
func (a *assembler) giveUp() bool {
	// The frame waited for is the first buffered after next.
	var waited *rtp.Packet
	for s, p := range a.buf {
		if waited == nil || int16(s-waited.SequenceNumber) < 0 {
			waited = p
		}
	}
	if waited == nil || len(a.buf) <= a.maxLate && a.newest-waited.Timestamp <= a.maxLateTS {
		return false
	}
	// Resume at the next frame start: a frame-start packet of another
	// timestamp.
	var resume *rtp.Packet
	for s, p := range a.buf {
		if p.Timestamp != waited.Timestamp && a.d.IsPartitionHead(p.Payload) &&
			(resume == nil || int16(s-resume.SequenceNumber) < 0) {
			resume = p
		}
	}
	if resume == nil {
		if len(a.buf) > a.maxLate { // No frame start at all: start over
			a.dropped += len(a.buf)
			clear(a.buf)
			a.started = false
		}
		return false
	}
	for s := a.next; s != resume.SequenceNumber; s++ {
		delete(a.buf, s)
		a.dropped++
	}
	a.next = resume.SequenceNumber
	return true
}
