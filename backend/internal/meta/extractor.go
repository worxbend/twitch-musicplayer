package meta

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	id3 "github.com/bogem/id3v2/v2"
)

// Info holds extracted metadata from an audio file.
type Info struct {
	Title        string
	Artist       string
	Album        string
	DurationSecs float64
	HasCover     bool
}

// maxCoverTagBytes caps the ID3v2 tag size whose pictures are parsed, so a
// hostile or oversized tag cannot force a large allocation during a scan or
// a cover request.
const maxCoverTagBytes = 8 << 20

const pictureFrame = "Attached picture"

var (
	// ErrNoCover reports that a file has no usable embedded cover image.
	ErrNoCover = errors.New("no embedded cover art")
	// ErrCoverTooLarge reports a tag over maxCoverTagBytes, which is not parsed.
	ErrCoverTooLarge = errors.New("id3v2 tag too large to read cover art")
)

// coverMIMEs are the image types served as cover art, keyed by the type
// http.DetectContentType reports. The tag's own MIME field is not trusted.
var coverMIMEs = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// Extract reads ID3v2 tags from path, returning best-effort metadata.
// Falls back to the filename as title, and to a duration read from the MPEG frame
// headers, when tags are absent.
func Extract(path string) Info {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	info := Info{Title: name}

	// Parse only the frames read below; pictures only when the tag is small
	// enough that loading them is bounded.
	frames := []string{"Title", "Artist", "Album/Movie/Show title", "Length"}
	if id3v2Size(path) <= maxCoverTagBytes {
		frames = append(frames, pictureFrame)
	}
	tag, err := id3.Open(path, id3.Options{Parse: true, ParseFrames: frames})
	if err != nil {
		return withDuration(info, path)
	}
	defer tag.Close()

	_, _, info.HasCover = coverPicture(tag)

	if t := cleanTag(tag.Title()); t != "" {
		info.Title = t
	}
	info.Artist = cleanTag(tag.Artist())
	info.Album = cleanTag(tag.Album())

	// TLEN frame encodes duration in milliseconds.
	if frames := tag.GetFrames(tag.CommonID("Length")); len(frames) > 0 {
		if tf, ok := frames[0].(id3.TextFrame); ok {
			if ms, err := strconv.ParseFloat(strings.TrimSpace(tf.Text), 64); err == nil && ms > 0 {
				info.DurationSecs = ms / 1000.0
				return info
			}
		}
	}

	return withDuration(info, path)
}

// cleanTag turns raw ID3 text into one display line. id3v2 strips only one
// trailing terminator, so ID3v2.4 multi-value frames ("A\x00B") and padded
// frames keep NULs. Each NUL-separated value is trimmed, with other control
// characters read as spaces; empty values are dropped and the rest joined
// with ", ". All-NUL or blank text becomes "".
func cleanTag(s string) string {
	values := strings.Split(s, "\x00")
	kept := values[:0]
	for _, v := range values {
		if v = strings.TrimSpace(strings.Map(controlToSpace, v)); v != "" {
			kept = append(kept, v)
		}
	}
	return strings.Join(kept, ", ")
}

func controlToSpace(r rune) rune {
	if unicode.IsControl(r) {
		return ' '
	}
	return r
}

// Cover returns the embedded cover image of the file at path and its sniffed
// MIME type. It returns ErrNoCover when the file has no usable picture and
// ErrCoverTooLarge, without parsing, when the tag exceeds maxCoverTagBytes.
func Cover(path string) (mime string, data []byte, err error) {
	return cover(path, maxCoverTagBytes)
}

func cover(path string, maxTagBytes int64) (string, []byte, error) {
	if size := id3v2Size(path); size > maxTagBytes {
		return "", nil, fmt.Errorf("%w: %d bytes", ErrCoverTooLarge, size)
	}
	tag, err := id3.Open(path, id3.Options{Parse: true, ParseFrames: []string{pictureFrame}})
	if err != nil {
		return "", nil, err
	}
	defer tag.Close()

	mime, data, ok := coverPicture(tag)
	if !ok {
		return "", nil, ErrNoCover
	}
	return mime, data, nil
}

// coverPicture picks the picture to serve from tag: the first usable front
// cover, else the first usable picture of any type. Usable means embedded
// (not a URL link) image bytes of a type listed in coverMIMEs.
func coverPicture(tag *id3.Tag) (mime string, data []byte, ok bool) {
	for _, framer := range tag.GetFrames(tag.CommonID(pictureFrame)) {
		pic, isPicture := framer.(id3.PictureFrame)
		if !isPicture || pic.MimeType == "-->" || len(pic.Picture) == 0 {
			continue
		}
		sniffed := http.DetectContentType(pic.Picture)
		if !coverMIMEs[sniffed] {
			continue
		}
		if pic.PictureType == id3.PTFrontCover {
			return sniffed, pic.Picture, true
		}
		if !ok {
			mime, data, ok = sniffed, pic.Picture, true
		}
	}
	return mime, data, ok
}

// withDuration fills DurationSecs from the MPEG frame headers (see
// mpegDuration), falling back to a rough 128 kbps CBR estimate when no frame
// is found. A leading ID3v2 and a trailing ID3v1 tag are excluded so embedded
// cover art and tag bytes do not count as audio.
func withDuration(info Info, path string) Info {
	f, err := os.Open(path)
	if err != nil {
		return info
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return info
	}
	audioStart := id3v2TagSize(f)
	audioEnd := stat.Size() - id3v1TagSize(f, stat.Size())
	if secs, ok := mpegDuration(f, audioStart, audioEnd); ok {
		info.DurationSecs = secs
	} else if audioBytes := audioEnd - audioStart; audioBytes > 0 {
		info.DurationSecs = float64(audioBytes) / 16_000 // 128 kbps = 16 000 B/s
	}
	return info
}

// id3v2Size returns the on-disk length of a leading ID3v2 tag in the file at
// path, or 0 when the file has none or cannot be read.
func id3v2Size(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	return id3v2TagSize(f)
}

// id3v2TagSize returns the length of a leading ID3v2 tag (header, frames,
// padding and footer), or 0 when r has none or cannot be read.
// tag.Size() is not used: it reflects re-serialized frames and omits padding.
func id3v2TagSize(r io.ReaderAt) int64 {
	var header [10]byte
	if n, _ := r.ReadAt(header[:], 0); n < len(header) || string(header[:3]) != "ID3" {
		return 0
	}
	var bodySize int64
	for _, b := range header[6:] { // syncsafe: 7 significant bits per byte
		bodySize = bodySize<<7 | int64(b&0x7f)
	}
	total := int64(len(header)) + bodySize
	if header[5]&0x10 != 0 { // footer present
		total += int64(len(header))
	}
	return total
}

// id3v1TagSize returns 128 when r, size bytes long, ends with an ID3v1 tag, else 0.
func id3v1TagSize(r io.ReaderAt, size int64) int64 {
	const tagLen = 128
	if size < tagLen {
		return 0
	}
	var magic [3]byte
	if n, _ := r.ReadAt(magic[:], size-tagLen); n < len(magic) || string(magic[:]) != "TAG" {
		return 0
	}
	return tagLen
}
