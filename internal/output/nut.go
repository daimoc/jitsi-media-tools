package output

import (
	"encoding/binary"
	"io"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
)

// nutWriter writes raw yuv420p video (stream 0) and s16le 48 kHz stereo
// audio (stream 1) as a NUT stream (libavformat/nut.txt), each packet
// stamped with its own time. With raw inputs ffmpeg would count frames and
// samples instead, and a stream that started later or lost a packet would
// stay behind the other.
type nutWriter struct {
	w       io.Writer
	width   int
	height  int
	started bool
	buf     []byte
}

// NUT constants (libavformat/nut.h).
const (
	nutMainStartcode      = 0x4E4D7A561F5F04AD
	nutStreamStartcode    = 0x4E5311405BF2F9DB
	nutSyncpointStartcode = 0x4E4BE4ADEECA4569
	nutFileID             = "nut/multimedia container\x00"

	nutFlagKey      = 1
	nutFlagCodedPTS = 8
	nutFlagStreamID = 16
	nutFlagSizeMSB  = 32
	nutFlagChecksum = 64
	nutFlagCoded    = 4096

	nutTimebase    = 90000 // One time base for both streams
	nutMSBPTSShift = 7
	nutMaxDistance = 65536
)

func newNUTWriter(w io.Writer, width, height int) *nutWriter {
	return &nutWriter{w: w, width: width, height: height}
}

// video writes a frame (width×height yuv420p) at time at.
func (n *nutWriter) video(at time.Duration, frame []byte) error { return n.frame(0, at, frame) }

// audio writes samples at time at.
func (n *nutWriter) audio(at time.Duration, samples []byte) error { return n.frame(1, at, samples) }

func (n *nutWriter) frame(stream int, at time.Duration, data []byte) error {
	b := n.buf[:0]
	if !n.started {
		b = n.headers(b)
		n.started = true
	}
	pts := uint64(at * nutTimebase / time.Second)
	// A syncpoint before every frame: a frame must start within
	// max_distance of the last one, and a video frame is far bigger.
	var sp []byte
	sp = putV(sp, pts) // global_key_pts, with one time base
	sp = putV(sp, 0)   // back_ptr_div16
	b = nutPacket(b, nutSyncpointStartcode, sp)

	start := len(b)
	b = append(b, 0) // Frame code 0: everything coded below
	b = putV(b, nutFlagKey|nutFlagStreamID|nutFlagCodedPTS|nutFlagSizeMSB|nutFlagChecksum)
	b = putV(b, uint64(stream))
	b = putV(b, pts+1<<nutMSBPTSShift) // Full pts
	b = putV(b, uint64(len(data)))     // size_mul 1, size_lsb 0
	b = binary.BigEndian.AppendUint32(b, nutCRC(b[start:]))
	n.buf = b
	if _, err := n.w.Write(b); err != nil {
		return err
	}
	_, err := n.w.Write(data)
	return err
}

// headers appends the file id, main header and stream headers.
func (n *nutWriter) headers(b []byte) []byte {
	b = append(b, nutFileID...)

	var m []byte
	m = putV(m, 3)              // Version
	m = putV(m, 2)              // Streams
	m = putV(m, nutMaxDistance) // max_distance
	m = putV(m, 1)              // Time bases
	m = putV(m, 1)
	m = putV(m, nutTimebase)
	// One frame code group: codes 0-255 but 'N', all fields coded in the
	// frame header.
	m = putV(m, nutFlagCoded) // Flags
	m = putV(m, 6)            // Fields: pts, mul, stream, size, reserved, count
	m = putV(m, 0)            // pts_delta (signed 0)
	m = putV(m, 1)            // size_mul
	m = putV(m, 0)            // stream
	m = putV(m, 0)            // size_lsb
	m = putV(m, 0)            // reserved
	m = putV(m, 255)          // count
	m = putV(m, 0)            // Elision headers beyond the empty one
	b = nutPacket(b, nutMainStartcode, m)

	var s []byte
	s = putV(s, 0) // Stream 0
	s = putV(s, 0) // Video
	s = putV(s, 4)
	s = append(s, "I420"...) // rawvideo yuv420p
	s = nutStreamCommon(s)
	s = putV(s, uint64(n.width))
	s = putV(s, uint64(n.height))
	s = putV(s, 0) // Aspect ratio unknown
	s = putV(s, 0)
	s = putV(s, 0) // Colorspace unknown
	b = nutPacket(b, nutStreamStartcode, s)

	s = s[:0]
	s = putV(s, 1) // Stream 1
	s = putV(s, 1) // Audio
	s = putV(s, 4)
	s = append(s, 'P', 'S', 'D', 16) // pcm_s16le
	s = nutStreamCommon(s)
	s = putV(s, ffmpeg.DecodedSampleRate)
	s = putV(s, 1) // Sample rate denominator
	s = putV(s, 2) // Channels
	return nutPacket(b, nutStreamStartcode, s)
}

// nutStreamCommon appends the stream header fields both streams share.
func nutStreamCommon(s []byte) []byte {
	s = putV(s, 0)              // Time base 0
	s = putV(s, nutMSBPTSShift) // msb_pts_shift
	s = putV(s, nutTimebase)    // max_pts_distance: 1 s
	s = putV(s, 0)              // decode_delay
	s = putV(s, 0)              // Stream flags
	return putV(s, 0)           // No codec extradata
}

// nutPacket appends a packet: startcode, forward pointer, content and its
// checksum.
func nutPacket(b []byte, startcode uint64, content []byte) []byte {
	start := len(b)
	b = binary.BigEndian.AppendUint64(b, startcode)
	fwd := uint64(len(content) + 4)
	b = putV(b, fwd)
	if fwd > 4096 {
		b = binary.BigEndian.AppendUint32(b, nutCRC(b[start:]))
	}
	b = append(b, content...)
	return binary.BigEndian.AppendUint32(b, nutCRC(content))
}

// putV appends a NUT variable-length unsigned number: 7 bits a byte, most
// significant first, the high bit set on all but the last.
func putV(b []byte, v uint64) []byte {
	n := 1
	for x := v >> 7; x != 0; x >>= 7 {
		n++
	}
	for i := n - 1; i > 0; i-- {
		b = append(b, 0x80|byte(v>>(7*i)))
	}
	return append(b, byte(v&0x7f))
}

// nutCRCTable is CRC-32 with polynomial 0x04C11DB7, most significant bit
// first (libavutil's AV_CRC_32_IEEE); NUT starts it at 0, without a final
// xor.
var nutCRCTable = func() (t [256]uint32) {
	for i := range t {
		c := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04C11DB7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

func nutCRC(b []byte) uint32 {
	var c uint32
	for _, x := range b {
		c = nutCRCTable[byte(c>>24)^x] ^ c<<8
	}
	return c
}
