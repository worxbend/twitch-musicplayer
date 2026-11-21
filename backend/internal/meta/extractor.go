package meta

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	id3 "github.com/bogem/id3v2/v2"
)

// Info holds extracted metadata from an audio file.
type Info struct {
	Title        string
	Artist       string
	Album        string
	DurationSecs float64
}

// Extract reads ID3v2 tags from path, returning best-effort metadata.
// Falls back to filename-based title and file-size-based duration when tags are absent.
func Extract(path string) Info {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	info := Info{Title: name}

	// Parse only the frames read below so cover art is not loaded during scans.
	tag, err := id3.Open(path, id3.Options{
		Parse:       true,
		ParseFrames: []string{"Title", "Artist", "Album/Movie/Show title", "Length"},
	})
	if err != nil {
		return withDurationEstimate(info, path)
	}
	defer tag.Close()

	if t := strings.TrimSpace(tag.Title()); t != "" {
		info.Title = t
	}
	info.Artist = strings.TrimSpace(tag.Artist())
	info.Album = strings.TrimSpace(tag.Album())

	// TLEN frame encodes duration in milliseconds.
	if frames := tag.GetFrames(tag.CommonID("Length")); len(frames) > 0 {
		if tf, ok := frames[0].(id3.TextFrame); ok {
			if ms, err := strconv.ParseFloat(strings.TrimSpace(tf.Text), 64); err == nil && ms > 0 {
				info.DurationSecs = ms / 1000.0
				return info
			}
		}
	}

	return withDurationEstimate(info, path)
}

// withDurationEstimate fills DurationSecs using a rough 128 kbps CBR assumption.
// The ID3v2 tag is excluded so embedded cover art does not inflate the estimate.
func withDurationEstimate(info Info, path string) Info {
	stat, err := os.Stat(path)
	if err != nil {
		return info
	}
	if audioBytes := stat.Size() - id3v2Size(path); audioBytes > 0 {
		info.DurationSecs = float64(audioBytes) / 16_000 // 128 kbps = 16 000 B/s
	}
	return info
}

// id3v2Size returns the on-disk length of a leading ID3v2 tag (header, frames,
// padding and footer), or 0 when the file has none or cannot be read.
// tag.Size() is not used: it reflects re-serialized frames and omits padding.
func id3v2Size(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	var header [10]byte
	if _, err := io.ReadFull(f, header[:]); err != nil || string(header[:3]) != "ID3" {
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
