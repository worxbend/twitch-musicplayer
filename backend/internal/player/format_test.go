package player

import "testing"

func TestHasNowPlayingPlaceholder(t *testing.T) {
	tests := []struct {
		format string
		want   bool
	}{
		{format: "{title}", want: true},
		{format: "{artist}", want: true},
		{format: "{album}", want: false},
		{format: "", want: false},
		{format: "{Title}", want: false},
	}
	for _, tt := range tests {
		if got := HasNowPlayingPlaceholder(tt.format); got != tt.want {
			t.Errorf("HasNowPlayingPlaceholder(%q) = %v, want %v", tt.format, got, tt.want)
		}
	}
}
