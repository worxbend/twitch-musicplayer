package meta

import (
	"bytes"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"testing"

	id3 "github.com/bogem/id3v2/v2"
)

// writeFixture writes fillerBytes zero bytes to dir/name and, when tagger is
// non-nil, prepends an ID3v2 tag built by tagger.
func writeFixture(t *testing.T, dir, name string, fillerBytes int, tagger func(*id3.Tag)) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, fillerBytes), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if tagger == nil {
		return path
	}
	tag, err := id3.Open(path, id3.Options{Parse: false})
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer tag.Close()
	tagger(tag)
	if err := tag.Save(); err != nil {
		t.Fatalf("save fixture tag: %v", err)
	}
	return path
}

func withLength(text string) func(*id3.Tag) {
	return func(tag *id3.Tag) {
		tag.SetTitle("Rain Walk")
		tag.SetArtist("Lofi Cat")
		tag.SetAlbum("Night Tapes")
		tag.AddTextFrame(tag.CommonID("Length"), id3.EncodingUTF8, text)
	}
}

func TestExtract(t *testing.T) {
	tests := []struct {
		name         string
		fillerBytes  int
		tagger       func(*id3.Tag)
		wantTitle    string
		wantArtist   string
		wantAlbum    string
		wantDuration float64
	}{
		{
			name:         "untagged file falls back to filename and size estimate",
			fillerBytes:  48_000,
			wantTitle:    "song-name",
			wantDuration: 3,
		},
		{
			name:         "TLEN gives duration",
			fillerBytes:  32_000,
			tagger:       withLength("185000"),
			wantTitle:    "Rain Walk",
			wantArtist:   "Lofi Cat",
			wantAlbum:    "Night Tapes",
			wantDuration: 185,
		},
		{
			name:        "tagged without TLEN estimates from audio bytes only",
			fillerBytes: 32_000,
			tagger: func(tag *id3.Tag) {
				tag.SetTitle("Rain Walk")
				tag.SetArtist("Lofi Cat")
				tag.SetAlbum("Night Tapes")
			},
			wantTitle:    "Rain Walk",
			wantArtist:   "Lofi Cat",
			wantAlbum:    "Night Tapes",
			wantDuration: 2,
		},
		{
			name:         "non-numeric TLEN falls back to estimate",
			fillerBytes:  32_000,
			tagger:       withLength("abc"),
			wantTitle:    "Rain Walk",
			wantArtist:   "Lofi Cat",
			wantAlbum:    "Night Tapes",
			wantDuration: 2,
		},
		{
			name:         "zero TLEN falls back to estimate",
			fillerBytes:  32_000,
			tagger:       withLength("0"),
			wantTitle:    "Rain Walk",
			wantArtist:   "Lofi Cat",
			wantAlbum:    "Night Tapes",
			wantDuration: 2,
		},
		{
			name:        "blank title falls back to filename and fields are trimmed",
			fillerBytes: 16_000,
			tagger: func(tag *id3.Tag) {
				tag.SetTitle("   ")
				tag.SetArtist("  Lofi Cat ")
				tag.SetAlbum(" Night Tapes  ")
			},
			wantTitle:    "song-name",
			wantArtist:   "Lofi Cat",
			wantAlbum:    "Night Tapes",
			wantDuration: 1,
		},
		{
			name:        "NUL separators are cleaned and an all-NUL title falls back to filename",
			fillerBytes: 16_000,
			tagger: func(tag *id3.Tag) {
				tag.SetTitle("\x00\x00")
				tag.SetArtist("Lofi Cat\x00Rain Fox")
				tag.SetAlbum("Night Tapes\x00\x00")
			},
			wantTitle:    "song-name",
			wantArtist:   "Lofi Cat, Rain Fox",
			wantAlbum:    "Night Tapes",
			wantDuration: 1,
		},
		{
			name:        "cover art is excluded from estimate",
			fillerBytes: 160_000,
			tagger: func(tag *id3.Tag) {
				tag.SetTitle("Rain Walk")
				tag.AddAttachedPicture(id3.PictureFrame{
					Encoding:    id3.EncodingUTF8,
					MimeType:    "image/jpeg",
					PictureType: id3.PTFrontCover,
					Picture:     make([]byte, 500_000),
				})
			},
			wantTitle:    "Rain Walk",
			wantDuration: 10,
		},
		{
			name:         "empty file has no duration",
			fillerBytes:  0,
			wantTitle:    "song-name",
			wantDuration: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeFixture(t, t.TempDir(), "song-name.mp3", tt.fillerBytes, tt.tagger)

			got := Extract(path)

			if got.Title != tt.wantTitle || got.Artist != tt.wantArtist || got.Album != tt.wantAlbum {
				t.Errorf("Extract() = %q/%q/%q, want %q/%q/%q",
					got.Title, got.Artist, got.Album, tt.wantTitle, tt.wantArtist, tt.wantAlbum)
			}
			if math.Abs(got.DurationSecs-tt.wantDuration) > 1e-9 {
				t.Errorf("DurationSecs = %v, want %v", got.DurationSecs, tt.wantDuration)
			}
		})
	}
}

func TestExtract_MissingFile(t *testing.T) {
	t.Parallel()
	got := Extract(filepath.Join(t.TempDir(), "gone.mp3"))

	if got != (Info{Title: "gone"}) {
		t.Errorf("Extract() = %+v, want title from filename and zero duration", got)
	}
}

func TestID3v2Size(t *testing.T) {
	tests := []struct {
		name   string
		header []byte
		want   int64
	}{
		{name: "syncsafe size", header: []byte("ID3\x04\x00\x00\x00\x00\x02\x01"), want: 10 + 257},
		{name: "footer flag adds 10", header: []byte("ID3\x04\x00\x10\x00\x00\x00\x05"), want: 10 + 5 + 10},
		{name: "no magic", header: []byte("RIFF\x00\x00\x00\x00\x00\x00"), want: 0},
		{name: "short file", header: []byte("ID3"), want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "song.mp3")
			if err := os.WriteFile(path, tt.header, 0o644); err != nil {
				t.Fatalf("write header: %v", err)
			}
			if got := id3v2Size(path); got != tt.want {
				t.Errorf("id3v2Size() = %d, want %d", got, tt.want)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		t.Parallel()
		if got := id3v2Size(filepath.Join(t.TempDir(), "gone.mp3")); got != 0 {
			t.Errorf("id3v2Size() = %d, want 0", got)
		}
	})
}

// Signature-prefixed payloads: http.DetectContentType sniffs only the header.
var (
	pngBytes  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x00\x01\x00\x00\x00\x01")
	jpegBytes = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01")
)

func withPictures(pictures ...id3.PictureFrame) func(*id3.Tag) {
	return func(tag *id3.Tag) {
		tag.SetTitle("Rain Walk")
		for _, pic := range pictures {
			pic.Encoding = id3.EncodingUTF8
			tag.AddAttachedPicture(pic)
		}
	}
}

func TestCover(t *testing.T) {
	tests := []struct {
		name      string
		tagger    func(*id3.Tag)
		wantMIME  string
		wantData  []byte
		wantError error
	}{
		{
			name:     "front cover png",
			tagger:   withPictures(id3.PictureFrame{MimeType: "image/png", PictureType: id3.PTFrontCover, Picture: pngBytes}),
			wantMIME: "image/png",
			wantData: pngBytes,
		},
		{
			name: "front cover preferred over earlier picture",
			tagger: withPictures(
				id3.PictureFrame{MimeType: "image/jpeg", PictureType: id3.PTOther, Picture: jpegBytes},
				id3.PictureFrame{MimeType: "image/png", PictureType: id3.PTFrontCover, Picture: pngBytes},
			),
			wantMIME: "image/png",
			wantData: pngBytes,
		},
		{
			name:     "non-front picture when no front cover, MIME sniffed from bytes",
			tagger:   withPictures(id3.PictureFrame{MimeType: "image/png", PictureType: id3.PTBackCover, Picture: jpegBytes}),
			wantMIME: "image/jpeg",
			wantData: jpegBytes,
		},
		{
			name: "unusable front cover falls back to usable picture",
			tagger: withPictures(
				id3.PictureFrame{MimeType: "image/png", PictureType: id3.PTFrontCover, Picture: []byte("not an image")},
				id3.PictureFrame{MimeType: "image/png", PictureType: id3.PTBackCover, Picture: pngBytes},
			),
			wantMIME: "image/png",
			wantData: pngBytes,
		},
		{
			name:      "non-image payload",
			tagger:    withPictures(id3.PictureFrame{MimeType: "image/png", PictureType: id3.PTFrontCover, Picture: []byte("<svg></svg>")}),
			wantError: ErrNoCover,
		},
		{
			name:      "url link picture",
			tagger:    withPictures(id3.PictureFrame{MimeType: "-->", PictureType: id3.PTFrontCover, Picture: pngBytes}),
			wantError: ErrNoCover,
		},
		{
			name:      "tag without picture",
			tagger:    withLength("1000"),
			wantError: ErrNoCover,
		},
		{
			name:      "untagged file",
			wantError: ErrNoCover,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeFixture(t, t.TempDir(), "song.mp3", 1_000, tt.tagger)

			mime, data, err := Cover(path)
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("Cover() error = %v, want %v", err, tt.wantError)
			}
			if mime != tt.wantMIME || !bytes.Equal(data, tt.wantData) {
				t.Errorf("Cover() = %q/%q, want %q/%q", mime, data, tt.wantMIME, tt.wantData)
			}
			if hasCover := Extract(path).HasCover; hasCover != (tt.wantError == nil) {
				t.Errorf("Extract().HasCover = %v, want %v", hasCover, tt.wantError == nil)
			}
		})
	}
}

func TestCover_TagOverCapIsNotParsed(t *testing.T) {
	t.Parallel()
	path := writeFixture(t, t.TempDir(), "song.mp3", 1_000,
		withPictures(id3.PictureFrame{MimeType: "image/png", PictureType: id3.PTFrontCover, Picture: pngBytes}))

	if _, _, err := cover(path, id3v2Size(path)-1); !errors.Is(err, ErrCoverTooLarge) {
		t.Fatalf("cover() error = %v, want ErrCoverTooLarge", err)
	}
	if _, _, err := cover(path, id3v2Size(path)); err != nil {
		t.Fatalf("cover() at the cap error = %v, want nil", err)
	}
}

func TestCover_MissingFile(t *testing.T) {
	t.Parallel()
	_, _, err := Cover(filepath.Join(t.TempDir(), "gone.mp3"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Cover() error = %v, want fs.ErrNotExist", err)
	}
}

func TestCleanTag(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "lone NUL", in: "\x00", want: ""},
		{name: "multi-value", in: "A\x00B", want: "A, B"},
		{name: "trailing NUL run", in: "A\x00\x00", want: "A"},
		{name: "spaces around NUL", in: " A \x00 ", want: "A"},
		{name: "inner tab", in: "A\tB", want: "A B"},
		{name: "plain text unchanged", in: "Lofi Cat", want: "Lofi Cat"},
		{name: "empty", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanTag(tt.in); got != tt.want {
				t.Errorf("cleanTag(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
