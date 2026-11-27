package meta

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
)

// mpegScanBytes bounds how far past the ID3v2 tag the first frame is sought.
const mpegScanBytes = 64 << 10

type mpegVersion int

const (
	mpeg1 mpegVersion = iota
	mpeg2
	mpeg25
)

// bitratesKbps is indexed by [MPEG1 = 0, MPEG2/2.5 = 1][layer-1][bitrate index].
// Index 0 (free format) and 15 (bad) hold 0 and are rejected.
var bitratesKbps = [2][3][16]int{
	{
		{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0},
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0},
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
	},
	{
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	},
}

// sampleRatesHz is indexed by [version][sample-rate index]; index 3 is reserved.
var sampleRatesHz = [3][3]int{
	mpeg1:  {44100, 48000, 32000},
	mpeg2:  {22050, 24000, 16000},
	mpeg25: {11025, 12000, 8000},
}

// frameHeader is a decoded 4-byte MPEG audio frame header.
type frameHeader struct {
	version     mpegVersion
	layer       int // 1, 2 or 3
	bitrateKbps int
	sampleRate  int
	padding     int // 0 or 1 slot
	mono        bool
}

// parseFrameHeader decodes the header at the start of b, rejecting reserved,
// free-format and bad field values.
func parseFrameHeader(b []byte) (frameHeader, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return frameHeader{}, false
	}
	var h frameHeader
	switch (b[1] >> 3) & 0x03 {
	case 0:
		h.version = mpeg25
	case 2:
		h.version = mpeg2
	case 3:
		h.version = mpeg1
	default:
		return frameHeader{}, false
	}
	h.layer = 4 - int((b[1]>>1)&0x03) // bits 11 = Layer I ... 01 = Layer III
	sampleRateIndex := (b[2] >> 2) & 0x03
	if h.layer == 4 || sampleRateIndex == 3 {
		return frameHeader{}, false
	}
	table := 1
	if h.version == mpeg1 {
		table = 0
	}
	h.bitrateKbps = bitratesKbps[table][h.layer-1][b[2]>>4]
	if h.bitrateKbps == 0 {
		return frameHeader{}, false
	}
	h.sampleRate = sampleRatesHz[h.version][sampleRateIndex]
	h.padding = int((b[2] >> 1) & 0x01)
	h.mono = b[3]>>6 == 0x03
	return h, true
}

func (h frameHeader) samplesPerFrame() int {
	switch {
	case h.layer == 1:
		return 384
	case h.layer == 3 && h.version != mpeg1:
		return 576
	default:
		return 1152
	}
}

// frameLen is the frame size in bytes. Layer I counts 4-byte slots; the
// others count bytes, at samplesPerFrame/8 bytes per bit/s of bitrate.
func (h frameHeader) frameLen() int {
	bitsPerSec := h.bitrateKbps * 1000
	if h.layer == 1 {
		return (12*bitsPerSec/h.sampleRate + h.padding) * 4
	}
	return h.samplesPerFrame()/8*bitsPerSec/h.sampleRate + h.padding
}

func (h frameHeader) sideInfoLen() int {
	switch {
	case h.version == mpeg1 && h.mono:
		return 17
	case h.version == mpeg1:
		return 32
	case h.mono:
		return 9
	default:
		return 17
	}
}

func (h frameHeader) sameStream(other frameHeader) bool {
	return h.version == other.version && h.layer == other.layer && h.sampleRate == other.sampleRate
}

// mpegDuration estimates the playing time of the MPEG audio stream stored in
// r between audioStart and audioEnd. A Xing, Info or VBRI frame count gives
// the exact length; otherwise the first frame's bitrate is taken as constant.
// It reports false when no confirmed frame header lies in the first
// mpegScanBytes of the stream.
func mpegDuration(r io.ReaderAt, audioStart, audioEnd int64) (float64, bool) {
	if audioStart < 0 || audioEnd <= audioStart {
		return 0, false
	}
	buf := make([]byte, min(audioEnd-audioStart, mpegScanBytes))
	n, err := r.ReadAt(buf, audioStart)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, false
	}
	buf = buf[:n]

	offset, h, ok := firstFrame(buf)
	if !ok {
		return 0, false
	}
	var secs float64
	if frames := vbrFrameCount(buf[offset:], h); frames > 0 {
		secs = float64(frames) * float64(h.samplesPerFrame()) / float64(h.sampleRate)
	} else {
		audioBytes := audioEnd - audioStart - int64(offset)
		secs = float64(audioBytes) * 8 / float64(h.bitrateKbps*1000)
	}
	if !(secs > 0) || math.IsInf(secs, 1) {
		return 0, false
	}
	return secs, true
}

// firstFrame returns the offset and header of the first frame in buf that is
// followed by a second header of the same stream, so a stray sync pattern in
// junk data is not mistaken for audio.
func firstFrame(buf []byte) (int, frameHeader, bool) {
	for offset := 0; offset+4 <= len(buf); offset++ {
		h, ok := parseFrameHeader(buf[offset:])
		if !ok {
			continue
		}
		next := offset + h.frameLen()
		if next > len(buf) {
			continue
		}
		if nextHeader, ok := parseFrameHeader(buf[next:]); ok && h.sameStream(nextHeader) {
			return offset, h, true
		}
	}
	return 0, frameHeader{}, false
}

// vbrFrameCount returns the frame count from a Xing/Info or VBRI tag in the
// first frame, or 0 when the frame carries none.
func vbrFrameCount(frame []byte, h frameHeader) uint32 {
	xing := 4 + h.sideInfoLen()
	if hasTagAt(frame, xing, "Xing") || hasTagAt(frame, xing, "Info") {
		const framesFlag = 0x01
		if uint32At(frame, xing+4)&framesFlag != 0 {
			return uint32At(frame, xing+8)
		}
	}
	const vbriOffset = 4 + 32
	if hasTagAt(frame, vbriOffset, "VBRI") {
		return uint32At(frame, vbriOffset+14)
	}
	return 0
}

func hasTagAt(b []byte, at int, tag string) bool {
	return at+len(tag) <= len(b) && string(b[at:at+len(tag)]) == tag
}

// uint32At reads a big-endian uint32 at b[at:], or 0 when b is too short.
func uint32At(b []byte, at int) uint32 {
	if at+4 > len(b) {
		return 0
	}
	return binary.BigEndian.Uint32(b[at:])
}
