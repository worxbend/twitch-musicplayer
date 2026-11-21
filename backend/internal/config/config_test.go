package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeEnv returns a getenv backed by a map, so tests never touch process env.
func fakeEnv(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func missingPath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "absent.toml")
}

func TestLoadFromDefaults(t *testing.T) {
	cfg, err := loadFrom(missingPath(t), fakeEnv(nil))
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Port != 8080 || cfg.Server.Port != 8080 {
		t.Errorf("port = %d/%d, want 8080", cfg.Port, cfg.Server.Port)
	}
	if cfg.BaseURL != "http://localhost:8080" || cfg.MusicDir != "./music" || cfg.BloomPath != "./lofi-radio.bloom" {
		t.Errorf("flat fields = %q %q %q", cfg.BaseURL, cfg.MusicDir, cfg.BloomPath)
	}
	if cfg.Shuffle.Strategy != "round_robin" || cfg.Queue.Strategy != "auto_refill" {
		t.Errorf("strategies = %q/%q", cfg.Shuffle.Strategy, cfg.Queue.Strategy)
	}
	if cfg.Queue.MinAhead != 1 || cfg.Queue.PreloadSize != 3 {
		t.Errorf("sizes = %d/%d, want 1/3", cfg.Queue.MinAhead, cfg.Queue.PreloadSize)
	}
}

func TestLoadFromTOMLOverridesDefaults(t *testing.T) {
	path := writeConfig(t, `
[server]
port = 9000
[music]
dir = "/srv/music"
extensions = ["mp3", ".flac"]
[shuffle]
strategy = "least_played"
[queue]
strategy = "preload"
preload_size = 5
`)
	cfg, err := loadFrom(path, fakeEnv(nil))
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Port != 9000 || cfg.MusicDir != "/srv/music" {
		t.Errorf("port/dir = %d/%q", cfg.Port, cfg.MusicDir)
	}
	if cfg.Shuffle.Strategy != "least_played" || cfg.Queue.Strategy != "preload" || cfg.Queue.PreloadSize != 5 {
		t.Errorf("shuffle/queue = %+v %+v", cfg.Shuffle, cfg.Queue)
	}
	if got := strings.Join(cfg.Music.Extensions, ","); got != ".mp3,.flac" {
		t.Errorf("extensions = %q, want .mp3,.flac", got)
	}
}

func TestLoadFromEnvOverridesTOML(t *testing.T) {
	path := writeConfig(t, `
[server]
port = 9000
base_url = "http://toml"
[music]
dir = "/toml/music"
[bloom]
path = "/toml/bloom"
`)
	cfg, err := loadFrom(path, fakeEnv(map[string]string{
		"PORT":       "9100",
		"MUSIC_DIR":  "/env/music",
		"BASE_URL":   "http://env",
		"BLOOM_PATH": "/env/bloom",
	}))
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Port != 9100 || cfg.MusicDir != "/env/music" || cfg.BaseURL != "http://env" || cfg.BloomPath != "/env/bloom" {
		t.Errorf("got port=%d dir=%q base=%q bloom=%q", cfg.Port, cfg.MusicDir, cfg.BaseURL, cfg.BloomPath)
	}
}

func TestLoadFromErrors(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		env     map[string]string
		wantSub []string
	}{
		{name: "malformed toml", body: "[server\nport = ", wantSub: []string{"parse ", "config.toml"}},
		{name: "unparsable PORT", env: map[string]string{"PORT": "80a"}, wantSub: []string{"PORT", `"80a"`}},
		{name: "invalid value", body: "[queue]\nstrategy = \"auto-refill\"\n", wantSub: []string{"queue.strategy"}},
		{name: "unknown section", body: "[database]\npath = \"x\"\n", wantSub: []string{"unknown config keys", "database.path"}},
		{name: "misspelled key", body: "[queue]\nmin_ahed = 2\n", wantSub: []string{"unknown config keys", "queue.min_ahed"}},
		{name: "top-level flat field", body: "Port = 9\n", wantSub: []string{"unknown config keys", "Port"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := missingPath(t)
			if tt.body != "" {
				path = writeConfig(t, tt.body)
			}
			_, err := loadFrom(path, fakeEnv(tt.env))
			if err == nil {
				t.Fatal("loadFrom succeeded, want error")
			}
			for _, sub := range tt.wantSub {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q does not contain %q", err, sub)
				}
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantSub []string
	}{
		{name: "bad shuffle strategy", mutate: func(c *Config) { c.Shuffle.Strategy = "shuffle" },
			wantSub: []string{`shuffle.strategy: unknown value "shuffle"`}},
		{name: "bad queue strategy", mutate: func(c *Config) { c.Queue.Strategy = "auto-refill" },
			wantSub: []string{`queue.strategy: unknown value "auto-refill" (want one of manual_only, auto_refill, preload)`}},
		{name: "port zero", mutate: func(c *Config) { c.Server.Port = 0 }, wantSub: []string{"server.port"}},
		{name: "port too high", mutate: func(c *Config) { c.Server.Port = 70000 }, wantSub: []string{"server.port"}},
		{name: "negative min_ahead", mutate: func(c *Config) { c.Queue.MinAhead = -1 }, wantSub: []string{"queue.min_ahead"}},
		{name: "negative preload_size", mutate: func(c *Config) { c.Queue.PreloadSize = -1 }, wantSub: []string{"queue.preload_size"}},
		{name: "negative recent_window", mutate: func(c *Config) { c.Shuffle.RecentWindow = -1 }, wantSub: []string{"shuffle.recent_window"}},
		{name: "empty music dir", mutate: func(c *Config) { c.Music.Dir = "" }, wantSub: []string{"music.dir"}},
		{name: "no extensions", mutate: func(c *Config) { c.Music.Extensions = nil }, wantSub: []string{"music.extensions"}},
		{name: "blank extension", mutate: func(c *Config) { c.Music.Extensions = []string{""} }, wantSub: []string{"music.extensions"}},
		{name: "multiple problems", mutate: func(c *Config) {
			c.Server.Port = 0
			c.Queue.Strategy = "nope"
			c.Queue.MinAhead = -1
		}, wantSub: []string{"server.port", "queue.strategy", "queue.min_ahead"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaults()
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate succeeded, want error")
			}
			for _, sub := range tt.wantSub {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q does not contain %q", err, sub)
				}
			}
		})
	}
}

func TestLoadFromNormalizesBaseURL(t *testing.T) {
	tests := []struct {
		name, toml, env, want string
	}{
		{name: "trailing slash trimmed", toml: "http://host:8080/", want: "http://host:8080"},
		{name: "path prefix kept", toml: "https://radio.example/sub/", want: "https://radio.example/sub"},
		{name: "spaces and slashes trimmed", toml: " http://host// ", want: "http://host"},
		{name: "env trailing slash trimmed", toml: "http://toml", env: "http://env:8080/", want: "http://env:8080"},
		{name: "empty allowed for relative stream urls", toml: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, "[server]\nbase_url = \""+tt.toml+"\"\n")
			cfg, err := loadFrom(path, fakeEnv(map[string]string{"BASE_URL": tt.env}))
			if err != nil {
				t.Fatalf("loadFrom: %v", err)
			}
			if cfg.Server.BaseURL != tt.want || cfg.BaseURL != tt.want {
				t.Errorf("base url = %q/%q, want %q", cfg.Server.BaseURL, cfg.BaseURL, tt.want)
			}
		})
	}
}

func TestLoadFromRejectsBadBaseURL(t *testing.T) {
	for _, bad := range []string{"localhost:8080", "ftp://x", "http://", "http://h?x=1", "http://h#f", "http://h?", "http://h/%zz"} {
		t.Run(bad, func(t *testing.T) {
			path := writeConfig(t, "[server]\nbase_url = \""+bad+"\"\n")
			_, err := loadFrom(path, fakeEnv(nil))
			if err == nil || !strings.Contains(err.Error(), "server.base_url: ") {
				t.Errorf("loadFrom error = %v, want one naming server.base_url", err)
			}
		})
	}
}

func TestValidateDefaultsOK(t *testing.T) {
	if err := defaults().Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
}

func TestShippedConfigsValidate(t *testing.T) {
	for _, name := range []string{"config.toml", "config.example.toml"} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadFrom(filepath.Join("..", "..", name), fakeEnv(nil)); err != nil {
				t.Fatalf("loadFrom %s: %v", name, err)
			}
		})
	}
}

func TestNormalizeExtensions(t *testing.T) {
	got := normalizeExtensions([]string{"mp3", ".flac", " ogg ", ""})
	want := []string{".mp3", ".flac", ".ogg", ""}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("normalizeExtensions = %q, want %q", got, want)
	}
}

func TestExpandHomePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tests := []struct{ in, want string }{
		{"~", home},
		{"~/x", filepath.Join(home, "x")},
		{"a~b", "a~b"},
		{"/abs/path", "/abs/path"},
	}
	for _, tt := range tests {
		if got := expandHomePath(tt.in); got != tt.want {
			t.Errorf("expandHomePath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLoadUsesLOFIConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, "[music]\ndir = \""+filepath.Join(dir, "music")+"\"\n")
	t.Setenv("LOFI_CONFIG", path)
	for _, key := range []string{"PORT", "MUSIC_DIR", "BASE_URL", "BLOOM_PATH"} {
		t.Setenv(key, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if info, err := os.Stat(cfg.MusicDir); err != nil || !info.IsDir() {
		t.Errorf("music dir %q not created: %v", cfg.MusicDir, err)
	}
}

func TestValidateOBS(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		obs     OBSConfig
		wantSub string // empty means Validate must pass
	}{
		{name: "empty file skips the dir check", obs: OBSConfig{NowPlayingFormat: "{title}"}},
		{name: "format without placeholders and no file", obs: OBSConfig{NowPlayingFormat: "no placeholders"},
			wantSub: "obs.now_playing_format"},
		{name: "existing dir", obs: OBSConfig{NowPlayingFile: filepath.Join(dir, "np.txt"), NowPlayingFormat: "{title}"}},
		{name: "missing dir", obs: OBSConfig{NowPlayingFile: filepath.Join(dir, "absent", "np.txt"), NowPlayingFormat: "{title}"},
			wantSub: "obs.now_playing_file"},
		{name: "format without placeholders", obs: OBSConfig{NowPlayingFile: filepath.Join(dir, "np.txt"), NowPlayingFormat: "{album}"},
			wantSub: "obs.now_playing_format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaults()
			cfg.OBS = tt.obs
			err := cfg.Validate()
			if tt.wantSub == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("Validate error = %v, want one naming %s", err, tt.wantSub)
			}
		})
	}
}

func TestLoadFromExpandsOBSFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := writeConfig(t, "[obs]\nnow_playing_file = \"~/np.txt\"\n")
	cfg, err := loadFrom(path, fakeEnv(nil))
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if want := filepath.Join(home, "np.txt"); cfg.OBS.NowPlayingFile != want {
		t.Errorf("now_playing_file = %q, want %q", cfg.OBS.NowPlayingFile, want)
	}
	if cfg.OBS.NowPlayingFormat != "{artist} - {title}" {
		t.Errorf("now_playing_format = %q, want default", cfg.OBS.NowPlayingFormat)
	}
}
