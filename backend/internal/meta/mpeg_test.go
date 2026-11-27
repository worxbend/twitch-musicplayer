package meta

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// testFrame describes an MPEG audio frame by its raw header fields. Sizes are
// worked out by hand from the ISO 11172-3 / 13818-3 formulas, not from
// mpeg.go, so a wrong production table or formula fails the tests.
type testFrame struct {
	version         byte // 3 = MPEG1, 2 = MPEG2, 0 = MPEG2.5, 1 = reserved
	layer           byte // 3 = Layer I, 2 = Layer II, 1 = Layer III, 0 = reserved
	bitrateIndex    byte
	sampleRateIndex byte
	padding         bool
	mono            bool
	size            int // frame length in bytes
}

var (
	mpeg1L3At320k    = testFrame{version: 3, layer: 1, bitrateIndex: 14, sampleRateIndex: 0, size: 1044} // 144*320000/44100
	mpeg1L3At128k    = testFrame{version: 3, layer: 1, bitrateIndex: 9, sampleRateIndex: 0, size: 417}   // 144*128000/44100
	mpeg1L3At128kPad = testFrame{version: 3, layer: 1, bitrateIndex: 9, sampleRateIndex: 0, padding: true, size: 418}
	mpeg1L3Mono      = testFrame{version: 3, layer: 1, bitrateIndex: 9, sampleRateIndex: 0, mono: true, size: 417}
	mpeg1L3At48kHz   = testFrame{version: 3, layer: 1, bitrateIndex: 9, sampleRateIndex: 1, size: 384}  // 144*128000/48000
	mpeg1L2At192k    = testFrame{version: 3, layer: 2, bitrateIndex: 10, sampleRateIndex: 1, size: 576} // 144*192000/48000
	mpeg1L1At384k    = testFrame{version: 3, layer: 3, bitrateIndex: 12, sampleRateIndex: 1, size: 384} // (12*384000/48000)*4
	mpeg2L3At64k     = testFrame{version: 2, layer: 1, bitrateIndex: 8, sampleRateIndex: 0, size: 208}  // 72*64000/22050
	mpeg2L3Mono      = testFrame{version: 2, layer: 1, bitrateIndex: 8, sampleRateIndex: 0, mono: true, size: 208}
	mpeg25L3At8k     = testFrame{version: 0, layer: 1, bitrateIndex: 1, sampleRateIndex: 2, size: 72} // 72*8000/8000
)

func (f testFrame) header() []byte {
	b1 := 0xE0 | f.version<<3 | f.layer<<1 | 0x01 // 0x01: no CRC
	b2 := f.bitrateIndex<<4 | f.sampleRateIndex<<2
	if f.padding {
		b2 |= 0x02
	}
	var b3 byte
	if f.mono {
		b3 = 0xC0
	}
	return []byte{0xFF, b1, b2, b3}
}

// frames returns n frames of f with zero payload.
func (f testFrame) frames(n int) []byte {
	stream := make([]byte, n*f.size)
	for i := range n {
		copy(stream[i*f.size:], f.header())
	}
	return stream
}

// withTagAt returns stream with tag written at offset into its first frame.
func withTagAt(stream []byte, offset int, tag []byte) []byte {
	copy(stream[offset:], tag)
	return stream
}

func be32(v uint32) []byte {
	return binary.BigEndian.AppendUint32(nil, v)
}

func xingTag(magic string, flags, frames uint32) []byte {
	return append(append([]byte(magic), be32(flags)...), be32(frames)...)
}

// vbriTag lays out magic, version, delay and quality (2 bytes each), the
// stream byte count, then the frame count at offset 14.
func vbriTag(frames uint32) []byte {
	tag := []byte("VBRI\x00\x01\x00\x00\x00\x4b")
	tag = append(tag, be32(1_000_000)...)
	return append(tag, be32(frames)...)
}

func concat(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

var junk = bytes.Repeat([]byte("lofi"), 4_000)

func cbrSecs(audioBytes, kbps int) float64 {
	return float64(audioBytes) * 8 / float64(kbps*1000)
}

func vbrSecs(frames, samplesPerFrame, sampleRate int) float64 {
	return float64(frames) * float64(samplesPerFrame) / float64(sampleRate)
}

func writeStream(t *testing.T, stream []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "song.mp3")
	if err := os.WriteFile(path, stream, 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	return path
}

func TestMpegDuration(t *testing.T) {
	tests := []struct {
		name   string
		stream []byte
		want   float64
		wantOK bool
	}{
		{
			name:   "320 kbps MPEG1 Layer III CBR",
			stream: mpeg1L3At320k.frames(1000),
			want:   cbrSecs(1000*1044, 320), // 26.1 s, within 1% of 1000*1152/44100
			wantOK: true,
		},
		{
			name:   "64 kbps MPEG2 Layer III CBR",
			stream: mpeg2L3At64k.frames(500),
			want:   cbrSecs(500*208, 64),
			wantOK: true,
		},
		{
			name:   "padded frames",
			stream: mpeg1L3At128kPad.frames(100),
			want:   cbrSecs(100*418, 128),
			wantOK: true,
		},
		{
			name:   "MPEG1 Layer II",
			stream: mpeg1L2At192k.frames(100),
			want:   cbrSecs(100*576, 192),
			wantOK: true,
		},
		{
			name:   "MPEG1 Layer I",
			stream: mpeg1L1At384k.frames(100),
			want:   cbrSecs(100*384, 384),
			wantOK: true,
		},
		{
			name:   "MPEG2.5 Layer III",
			stream: mpeg25L3At8k.frames(100),
			want:   cbrSecs(100*72, 8),
			wantOK: true,
		},
		{
			name:   "junk before the first frame is skipped",
			stream: concat(junk[:101], mpeg1L3At320k.frames(10)),
			want:   cbrSecs(10*1044, 320),
			wantOK: true,
		},
		{
			name:   "Xing frame count, MPEG1 stereo",
			stream: withTagAt(mpeg1L3At128k.frames(10), 4+32, xingTag("Xing", 0x0F, 500)),
			want:   vbrSecs(500, 1152, 44100),
			wantOK: true,
		},
		{
			name:   "Xing frame count, MPEG1 mono",
			stream: withTagAt(mpeg1L3Mono.frames(10), 4+17, xingTag("Xing", 0x01, 200)),
			want:   vbrSecs(200, 1152, 44100),
			wantOK: true,
		},
		{
			name:   "Xing frame count, MPEG2 stereo",
			stream: withTagAt(mpeg2L3At64k.frames(10), 4+17, xingTag("Xing", 0x01, 300)),
			want:   vbrSecs(300, 576, 22050),
			wantOK: true,
		},
		{
			name:   "Xing frame count, MPEG2 mono",
			stream: withTagAt(mpeg2L3Mono.frames(10), 4+9, xingTag("Xing", 0x01, 300)),
			want:   vbrSecs(300, 576, 22050),
			wantOK: true,
		},
		{
			name:   "Info tag is honoured like Xing",
			stream: withTagAt(mpeg1L3At128k.frames(10), 4+32, xingTag("Info", 0x01, 500)),
			want:   vbrSecs(500, 1152, 44100),
			wantOK: true,
		},
		{
			name:   "Xing without the frames flag falls back to the bitrate",
			stream: withTagAt(mpeg1L3At128k.frames(10), 4+32, xingTag("Xing", 0x0E, 500)),
			want:   cbrSecs(10*417, 128),
			wantOK: true,
		},
		{
			name:   "VBRI frame count",
			stream: withTagAt(mpeg1L3At128k.frames(10), 4+32, vbriTag(300)),
			want:   vbrSecs(300, 1152, 44100),
			wantOK: true,
		},
		{
			name:   "no sync in junk",
			stream: junk,
		},
		{
			name:   "lone false sync followed by junk",
			stream: concat(mpeg1L3At128k.header(), junk),
		},
		{
			name:   "next header from another stream",
			stream: concat(mpeg1L3At128k.frames(1), mpeg1L3At48kHz.header(), junk[:400]),
		},
		{
			name:   "reserved version",
			stream: testFrame{version: 1, layer: 1, bitrateIndex: 9, size: 417}.frames(3),
		},
		{
			name:   "reserved layer",
			stream: testFrame{version: 3, layer: 0, bitrateIndex: 9, size: 417}.frames(3),
		},
		{
			name:   "free-format bitrate",
			stream: testFrame{version: 3, layer: 1, bitrateIndex: 0, size: 417}.frames(3),
		},
		{
			name:   "bad bitrate index",
			stream: testFrame{version: 3, layer: 1, bitrateIndex: 15, size: 417}.frames(3),
		},
		{
			name:   "reserved sample rate",
			stream: testFrame{version: 3, layer: 1, bitrateIndex: 9, sampleRateIndex: 3, size: 417}.frames(3),
		},
		{
			name:   "single frame has nothing to confirm it",
			stream: mpeg1L3At128k.frames(1),
		},
		{
			name:   "truncated mid header",
			stream: mpeg1L3At128k.header()[:3],
		},
		{
			name:   "truncated mid second header",
			stream: mpeg1L3At128k.frames(2)[:417+2],
		},
		{
			name: "empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, err := os.Open(writeStream(t, tt.stream))
			if err != nil {
				t.Fatalf("open stream: %v", err)
			}
			defer f.Close()

			got, ok := mpegDuration(f, 0, int64(len(tt.stream)))

			if ok != tt.wantOK || math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("mpegDuration() = %v, %v, want %v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestMpegDuration_BadRange(t *testing.T) {
	t.Parallel()
	stream := bytes.NewReader(mpeg1L3At128k.frames(10))
	ranges := [][2]int64{{-1, 4170}, {4170, 4170}, {100, 50}, {5000, 6000}}
	for _, r := range ranges {
		if got, ok := mpegDuration(stream, r[0], r[1]); ok || got != 0 {
			t.Errorf("mpegDuration(%d, %d) = %v, %v, want 0, false", r[0], r[1], got, ok)
		}
	}
}

func TestExtract_MPEGDuration(t *testing.T) {
	id3v2 := concat([]byte("ID3\x03\x00\x00\x00\x00\x00\x64"), make([]byte, 100))
	id3v1 := concat([]byte("TAG"), make([]byte, 125))
	tests := []struct {
		name   string
		stream []byte
		want   float64
	}{
		{
			name:   "ID3v2 and ID3v1 tags are not counted as audio",
			stream: concat(id3v2, mpeg1L3At320k.frames(50), id3v1),
			want:   cbrSecs(50*1044, 320),
		},
		{
			name:   "no frame falls back to 128 kbps",
			stream: junk,
			want:   float64(len(junk)) / 16_000,
		},
		{
			name:   "fallback excludes the ID3v1 tag",
			stream: concat(junk, id3v1),
			want:   float64(len(junk)) / 16_000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Extract(writeStream(t, tt.stream)).DurationSecs

			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("DurationSecs = %v, want %v", got, tt.want)
			}
		})
	}
}

func FuzzMpegDuration(f *testing.F) {
	f.Add(mpeg1L3At320k.frames(3))
	f.Add(withTagAt(mpeg1L3At128k.frames(3), 4+32, xingTag("Xing", 0x01, 500)))
	f.Add(withTagAt(mpeg1L3At128k.frames(3), 4+32, vbriTag(300)))
	f.Fuzz(func(t *testing.T, stream []byte) {
		got, ok := mpegDuration(bytes.NewReader(stream), 0, int64(len(stream)))

		if math.IsNaN(got) || math.IsInf(got, 0) || got < 0 || ok != (got > 0) {
			t.Fatalf("mpegDuration() = %v, %v, want finite, positive exactly when ok", got, ok)
		}
	})
}
