package config

import (
	"errors"
	"fmt"
	"log"
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

	// Flat accessors populated by Load.
	Port      int
	MusicDir  string
	BloomPath string
	BaseURL   string
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

func defaults() *Config {
	return &Config{
		Server:  ServerConfig{Port: 8080, BaseURL: "http://localhost:8080"},
		Music:   MusicConfig{Dir: "./music", Extensions: []string{".mp3", ".MP3"}},
		Bloom:   BloomConfig{Path: "./lofi-radio.bloom"},
		Shuffle: ShuffleConfig{Strategy: "round_robin", RecentWindow: 0},
		Queue:   QueueConfig{Strategy: "auto_refill", MinAhead: 1, PreloadSize: 3},
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
		if _, err := toml.DecodeFile(path, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
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
	cfg.Music.Extensions = normalizeExtensions(cfg.Music.Extensions)

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
	)
	return errors.Join(errs...)
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
