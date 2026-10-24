package rtpmedia

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// OggOpusReader reads Opus packets from an Ogg stream. Unlike Pion's
// oggreader, which returns whole pages, it splits pages into packets by
// their lacing values, since ffmpeg may put several 20 ms packets on one
// page. The OpusHead and OpusTags headers are skipped.
type OggOpusReader struct {
	r       *bufio.Reader
	pending [][]byte // Complete packets of the current page
	partial []byte   // Packet continued on the next page
	headers int      // Header packets seen (2 expected)
}

// NewOggOpusReader reads Opus from r.
func NewOggOpusReader(r io.Reader) *OggOpusReader {
	return &OggOpusReader{r: bufio.NewReader(r)}
}

// Next returns the next Opus packet as a 20 ms frame; io.EOF at the end.
func (o *OggOpusReader) Next() (Frame, error) {
	for {
		for len(o.pending) > 0 {
			p := o.pending[0]
			o.pending = o.pending[1:]
			if o.headers < 2 && (bytes.HasPrefix(p, []byte("OpusHead")) || bytes.HasPrefix(p, []byte("OpusTags"))) {
				o.headers++
				continue
			}
			if len(p) == 0 {
				continue
			}
			return Frame{Data: p, Duration: OpusFrame, Samples: OpusSamples}, nil
		}
		if err := o.readPage(); err != nil {
			return Frame{}, err
		}
	}
}

// readPage reads one page and appends its complete packets to pending.
func (o *OggOpusReader) readPage() error {
	var h [27]byte
	if _, err := io.ReadFull(o.r, h[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return io.EOF
		}
		return err
	}
	if string(h[:4]) != "OggS" {
		return fmt.Errorf("ogg: bad capture pattern %q", h[:4])
	}
	segments := int(h[26])
	lacing := make([]byte, segments)
	if _, err := io.ReadFull(o.r, lacing); err != nil {
		return eofIfShort(err)
	}
	size := 0
	for _, l := range lacing {
		size += int(l)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(o.r, body); err != nil {
		return eofIfShort(err)
	}
	off := 0
	for _, l := range lacing {
		o.partial = append(o.partial, body[off:off+int(l)]...)
		off += int(l)
		if l < 255 {
			o.pending = append(o.pending, o.partial)
			o.partial = nil
		}
	}
	return nil
}

func eofIfShort(err error) error {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return io.EOF
	}
	return err
}
