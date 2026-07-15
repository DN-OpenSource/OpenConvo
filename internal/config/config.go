// Package config loads OpenStream configuration from YAML files,
// OPENSTREAM_* environment variables and command-line flags, with
// precedence flags > env > yaml > defaults (SPEC.md §3.1, §18).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the typed configuration for every OpenStream service.
type Config struct {
	// HTTPAddr is the listen address for the REST API.
	HTTPAddr string `yaml:"http_addr"`
	// WSAddr is the listen address for the realtime WebSocket service.
	// When equal to HTTPAddr (default) both are served from one listener.
	WSAddr string `yaml:"ws_addr"`

	PostgresDSN string `yaml:"postgres_dsn"`
	RedisAddr   string `yaml:"redis_addr"`
	NATSURL     string `yaml:"nats_url"`

	Storage StorageConfig `yaml:"storage"`
	Search  SearchConfig  `yaml:"search"`

	LogLevel  string `yaml:"log_level"`  // debug | info | warn | error
	LogFormat string `yaml:"log_format"` // json | text

	// Auth
	DisableAuthChecks bool `yaml:"disable_auth_checks"`

	// Rate limiting (SPEC.md §9.3)
	RateLimitAppPerMin        int `yaml:"rate_limit_app_per_min"`
	RateLimitUserWritesPerMin int `yaml:"rate_limit_user_writes_per_min"`

	// Realtime (SPEC.md §8.1)
	WSHeartbeatInterval time.Duration `yaml:"ws_heartbeat_interval"`
	WSDeadTimeout       time.Duration `yaml:"ws_dead_timeout"`
	PresenceDebounce    time.Duration `yaml:"presence_debounce"`

	// Outbox relay
	OutboxBatchSize    int           `yaml:"outbox_batch_size"`
	OutboxPollInterval time.Duration `yaml:"outbox_poll_interval"`
}

// StorageConfig configures the object-storage adapter (SPEC.md §15).
type StorageConfig struct {
	Driver       string `yaml:"driver"` // s3 | local
	Endpoint     string `yaml:"endpoint"`
	Bucket       string `yaml:"bucket"`
	AccessKey    string `yaml:"access_key"`
	SecretKey    string `yaml:"secret_key"`
	Region       string `yaml:"region"`
	LocalDir     string `yaml:"local_dir"`
	UsePathStyle bool   `yaml:"use_path_style"`
}

// SearchConfig configures the search backend (SPEC.md §14).
type SearchConfig struct {
	Driver         string `yaml:"driver"` // meilisearch | pgfts | none
	MeilisearchURL string `yaml:"meilisearch_url"`
	MeilisearchKey string `yaml:"meilisearch_key"`
}

// Default returns the configuration defaults suitable for local development.
func Default() Config {
	return Config{
		HTTPAddr:                  ":3030",
		WSAddr:                    "",
		PostgresDSN:               "postgres://openstream:openstream@localhost:5432/openstream?sslmode=disable",
		RedisAddr:                 "localhost:6379",
		NATSURL:                   "nats://localhost:4222",
		Storage:                   StorageConfig{Driver: "local", LocalDir: "./data/uploads", UsePathStyle: true},
		Search:                    SearchConfig{Driver: "none", MeilisearchURL: "http://localhost:7700"},
		LogLevel:                  "info",
		LogFormat:                 "json",
		RateLimitAppPerMin:        5000,
		RateLimitUserWritesPerMin: 60,
		WSHeartbeatInterval:       25 * time.Second,
		WSDeadTimeout:             60 * time.Second,
		PresenceDebounce:          10 * time.Second,
		OutboxBatchSize:           100,
		OutboxPollInterval:        200 * time.Millisecond,
	}
}

// Load builds the effective configuration: defaults, then the YAML file at
// path (skipped when path is empty and the default file does not exist),
// then OPENSTREAM_* environment variables. Flag overrides are applied by
// the caller after Load.
func Load(path string) (Config, error) {
	cfg := Default()

	explicit := path != ""
	if path == "" {
		path = "openstream.yaml"
	}
	data, err := os.ReadFile(path) // #nosec G304 -- operator-supplied config path is the feature

	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("config: parse %s: %w", path, err)
		}
	case os.IsNotExist(err) && !explicit:
		// optional default file
	default:
		return cfg, fmt.Errorf("config: read %s: %w", path, err)
	}

	if err := cfg.applyEnv(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c *Config) applyEnv() error {
	str := func(key string, dst *string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	str("OPENSTREAM_HTTP_ADDR", &c.HTTPAddr)
	str("OPENSTREAM_WS_ADDR", &c.WSAddr)
	str("OPENSTREAM_POSTGRES_DSN", &c.PostgresDSN)
	str("OPENSTREAM_REDIS_ADDR", &c.RedisAddr)
	str("OPENSTREAM_NATS_URL", &c.NATSURL)
	str("OPENSTREAM_STORAGE_DRIVER", &c.Storage.Driver)
	str("OPENSTREAM_STORAGE_ENDPOINT", &c.Storage.Endpoint)
	str("OPENSTREAM_STORAGE_BUCKET", &c.Storage.Bucket)
	str("OPENSTREAM_STORAGE_ACCESS_KEY", &c.Storage.AccessKey)
	str("OPENSTREAM_STORAGE_SECRET_KEY", &c.Storage.SecretKey)
	str("OPENSTREAM_STORAGE_REGION", &c.Storage.Region)
	str("OPENSTREAM_STORAGE_LOCAL_DIR", &c.Storage.LocalDir)
	str("OPENSTREAM_SEARCH_DRIVER", &c.Search.Driver)
	str("OPENSTREAM_MEILISEARCH_URL", &c.Search.MeilisearchURL)
	str("OPENSTREAM_MEILISEARCH_KEY", &c.Search.MeilisearchKey)
	str("OPENSTREAM_LOG_LEVEL", &c.LogLevel)
	str("OPENSTREAM_LOG_FORMAT", &c.LogFormat)

	if v, ok := os.LookupEnv("OPENSTREAM_DISABLE_AUTH_CHECKS"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("config: OPENSTREAM_DISABLE_AUTH_CHECKS: %w", err)
		}
		c.DisableAuthChecks = b
	}
	ints := map[string]*int{
		"OPENSTREAM_RATE_LIMIT_APP_PER_MIN":         &c.RateLimitAppPerMin,
		"OPENSTREAM_RATE_LIMIT_USER_WRITES_PER_MIN": &c.RateLimitUserWritesPerMin,
		"OPENSTREAM_OUTBOX_BATCH_SIZE":              &c.OutboxBatchSize,
	}
	for key, dst := range ints {
		if v, ok := os.LookupEnv(key); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("config: %s: %w", key, err)
			}
			*dst = n
		}
	}
	return nil
}

// Validate reports configuration errors that would prevent startup.
func (c Config) Validate() error {
	var problems []string
	if c.HTTPAddr == "" {
		problems = append(problems, "http_addr is required")
	}
	if c.PostgresDSN == "" {
		problems = append(problems, "postgres_dsn is required")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, fmt.Sprintf("log_level %q invalid (debug|info|warn|error)", c.LogLevel))
	}
	if len(problems) > 0 {
		return fmt.Errorf("config: %s", strings.Join(problems, "; "))
	}
	return nil
}
