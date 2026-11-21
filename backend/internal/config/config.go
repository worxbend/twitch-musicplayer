package config

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	_ "github.com/joho/godotenv/autoload"
)

// Config holds the complete runtime configuration for the service.
type Config struct {
	Server  ServerConfig  `toml:"server"`
	Music   MusicConfig   `toml:"music"`
	Bloom   BloomConfig   `toml:"bloom"`
	Shuffle ShuffleConfig `toml:"shuffle"`
	Queue   QueueConfig   `toml:"queue"`
	OBS     OBSConfig     `toml:"obs"`

	// Flat accessors populated by Load. Tagged "-" so a top-level TOML key
	// such as `Port = 9` is reported as unknown instead of decoding here.
	Port      int    `toml:"-"`
	MusicDir  string `toml:"-"`
	BloomPath string `toml:"-"`
	BaseURL   string `toml:"-"`
}

type ServerConfig struct {
	Port    int    `toml:"port"`
	BaseURL string `toml:"base_url"`
}

type MusicConfig struct {
	Dir        string   `toml:"dir"`
	Extensions []string `toml:"extensions"`
}

type BloomConfig struct {
	// Path is where the played-songs Bloom filter is persisted between restarts.
	Path string `toml:"path"`
}

type ShuffleConfig struct {
	// Strategy: random | weighted_history | round_robin | least_played
	Strategy     string `toml:"strategy"`
	RecentWindow int    `toml:"recent_window"`
}

type QueueConfig struct {
	// Strategy: manual_only | auto_refill | preload
	Strategy    string `toml:"strategy"`
	MinAhead    int    `toml:"min_ahead"`
	PreloadSize int    `toml:"preload_size"`
}

// OBSConfig drives the optional now-playing text file for an OBS Text source.
type OBSConfig struct {
	// NowPlayingFile is rewritten on every song change. Empty disables it.
	NowPlayingFile string `toml:"now_playing_file"`
	// NowPlayingFormat accepts the {title}, {artist} and {album} placeholders.
	NowPlayingFormat string `toml:"now_playing_format"`
}

func defaults() *Config {
	return &Config{
		Server:  ServerConfig{Port: 8080, BaseURL: "http://localhost:8080"},
		Music:   MusicConfig{Dir: "./music", Extensions: []string{".mp3", ".MP3"}},
		Bloom:   BloomConfig{Path: "./lofi-radio.bloom"},
		Shuffle: ShuffleConfig{Strategy: "round_robin", RecentWindow: 0},
		Queue:   QueueConfig{Strategy: "auto_refill", MinAhead: 1, PreloadSize: 3},
		OBS:     OBSConfig{NowPlayingFormat: "{artist} - {title}"},
	}
}

// Allowed values for the strategy keys. Kept local so config does not import
// queue; they must match the queue package's strategy constants.
var (
	shuffleStrategies = []string{"random", "weighted_history", "round_robin", "least_played"}
	queueStrategies   = []string{"manual_only", "auto_refill", "preload"}
)

// Load reads config.toml (or the path in LOFI_CONFIG), overlays env vars,
// validates the result and ensures the music directory exists.
func Load() (*Config, error) {
	configPath := os.Getenv("LOFI_CONFIG")
	if configPath == "" {
		configPath = "config.toml"
	}

	cfg, err := loadFrom(configPath, os.Getenv)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(cfg.Music.Dir, 0o755); err != nil {
		log.Printf("warning: could not create music dir %s: %v", cfg.Music.Dir, err)
	}
	return cfg, nil
}

// loadFrom builds a validated Config from the TOML file at path (a missing
// file is not an error) overlaid with environment values read via getenv.
func loadFrom(path string, getenv func(string) string) (*Config, error) {
	cfg := defaults()

	if _, err := os.Stat(path); err == nil {
		md, err := toml.DecodeFile(path, cfg)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if err := checkNoUnknownKeys(path, md); err != nil {
			return nil, err
		}
		log.Printf("config loaded from %s", path)
	}

	// Env vars take precedence (container-friendly overrides).
	if v := getenv("PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("PORT: invalid value %q: not an integer", v)
		}
		cfg.Server.Port = n
	}
	if v := getenv("MUSIC_DIR"); v != "" {
		cfg.Music.Dir = v
	}
	if v := getenv("BASE_URL"); v != "" {
		cfg.Server.BaseURL = v
	}
	if v := getenv("BLOOM_PATH"); v != "" {
		cfg.Bloom.Path = v
	}

	cfg.Music.Dir = expandHomePath(cfg.Music.Dir)
	cfg.Bloom.Path = expandHomePath(cfg.Bloom.Path)
	cfg.OBS.NowPlayingFile = expandHomePath(cfg.OBS.NowPlayingFile)
	cfg.Music.Extensions = normalizeExtensions(cfg.Music.Extensions)
	cfg.Server.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.Server.BaseURL), "/")

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Populate flat fields.
	cfg.Port = cfg.Server.Port
	cfg.MusicDir = cfg.Music.Dir
	cfg.BloomPath = cfg.Bloom.Path
	cfg.BaseURL = cfg.Server.BaseURL

	return cfg, nil
}

// Validate reports every invalid setting at once, each message naming its TOML key.
func (c *Config) Validate() error {
	var errs []error
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		errs = append(errs, fmt.Errorf("server.port: %d out of range (want 1..65535)", c.Server.Port))
	}
	if strings.TrimSpace(c.Music.Dir) == "" {
		errs = append(errs, errors.New("music.dir: must not be empty"))
	}
	if len(c.Music.Extensions) == 0 {
		errs = append(errs, errors.New("music.extensions: must list at least one extension"))
	}
	if slices.Contains(c.Music.Extensions, "") {
		errs = append(errs, errors.New("music.extensions: must not contain an empty entry"))
	}
	errs = append(errs,
		checkOneOf("shuffle.strategy", c.Shuffle.Strategy, shuffleStrategies),
		checkOneOf("queue.strategy", c.Queue.Strategy, queueStrategies),
		checkNonNegative("shuffle.recent_window", c.Shuffle.RecentWindow),
		checkNonNegative("queue.min_ahead", c.Queue.MinAhead),
		checkNonNegative("queue.preload_size", c.Queue.PreloadSize),
		checkBaseURL("server.base_url", c.Server.BaseURL),
	)
	errs = append(errs, checkOBS(c.OBS)...)
	return errors.Join(errs...)
}

// checkOBS validates the now-playing settings. The format is always checked,
// because GET /v1/now-playing.txt uses it too; the directory only when the
// file is set.
func checkOBS(obs OBSConfig) []error {
	var errs []error
	if !strings.Contains(obs.NowPlayingFormat, "{title}") && !strings.Contains(obs.NowPlayingFormat, "{artist}") {
		errs = append(errs, fmt.Errorf("obs.now_playing_format: %q must contain {title} or {artist}", obs.NowPlayingFormat))
	}
	if obs.NowPlayingFile == "" {
		return errs
	}
	dir := filepath.Dir(obs.NowPlayingFile)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		errs = append(errs, fmt.Errorf("obs.now_playing_file: directory %q does not exist", dir))
	}
	return errs
}

// checkNoUnknownKeys rejects keys the Config struct does not define, so a
// typo or a removed section fails startup instead of being ignored.
func checkNoUnknownKeys(path string, md toml.MetaData) error {
	undecoded := md.Undecoded()
	if len(undecoded) == 0 {
		return nil
	}
	keys := make([]string, len(undecoded))
	for i, key := range undecoded {
		keys[i] = key.String()
	}
	return fmt.Errorf("%s: unknown config keys: %s", path, strings.Join(keys, ", "))
}

func checkOneOf(key, value string, allowed []string) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	return fmt.Errorf("%s: unknown value %q (want one of %s)", key, value, strings.Join(allowed, ", "))
}

func checkNonNegative(key string, value int) error {
	if value >= 0 {
		return nil
	}
	return fmt.Errorf("%s: %d must not be negative", key, value)
}

// checkBaseURL accepts an absolute http(s) origin with an optional path prefix.
// Empty is allowed: stream URLs then stay relative ("/v1/songs/...") and the
// frontend resolves them against its own backend URL.
func checkBaseURL(key, value string) error {
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("%s: %q is not a valid URL: %w", key, value, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s: %q must start with http:// or https://", key, value)
	}
	if u.Host == "" {
		return fmt.Errorf("%s: %q has no host", key, value)
	}
	// ContainsAny also catches a bare "?" or "#", which leave RawQuery and
	// Fragment empty yet still break the appended "/v1/songs/..." path.
	if strings.ContainsAny(value, "?#") {
		return fmt.Errorf("%s: %q must not contain a query or fragment", key, value)
	}
	return nil
}

// normalizeExtensions trims each extension and ensures it starts with '.',
// so "mp3" becomes ".mp3". Blank entries stay blank for Validate to report.
func normalizeExtensions(exts []string) []string {
	out := make([]string, 0, len(exts))
	for _, ext := range exts {
		ext = strings.TrimSpace(ext)
		if ext != "" && !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		out = append(out, ext)
	}
	return out
}

// expandHomePath rewrites a leading "~" into the user's home directory, e.g.
// "~/Music" becomes "/home/alice/Music". Anything else is returned unchanged,
// as is "~" itself when the home directory cannot be determined.
func expandHomePath(path string) string {
	isHome := path == "~"
	isUnderHome := strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`)
	if !isHome && !isUnderHome {
		return path
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if isHome {
		return home
	}
	return filepath.Join(home, path[2:])
}
