// Package config loads and validates the edge-limiter YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/HappyVirgo/edge-limiter/limiter"
)

// Config is the top-level service configuration.
type Config struct {
	Listen          string        `yaml:"listen"`
	LogLevel        string        `yaml:"log_level"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	TenantHeader    string        `yaml:"tenant_header"`
	Backend         Backend       `yaml:"backend"`
	Default         limiter.Limit `yaml:"default"`
	Rules           []Rule        `yaml:"rules"`
}

// Backend selects and tunes the storage backend.
type Backend struct {
	Type        string              `yaml:"type"` // "memory" or "redis"
	FailureMode limiter.FailureMode `yaml:"failure_mode"`
	Cooldown    time.Duration       `yaml:"cooldown"`
	Redis       Redis               `yaml:"redis"`
}

// Redis holds connection settings. The password is only read from the
// EDGE_LIMITER_REDIS_PASSWORD environment variable, never from the file.
type Redis struct {
	Addr      string        `yaml:"addr"`
	DB        int           `yaml:"db"`
	TLS       bool          `yaml:"tls"`
	Timeout   time.Duration `yaml:"timeout"`
	KeyPrefix string        `yaml:"key_prefix"`
	Password  string        `yaml:"-"`
}

// Rule applies Limit to requests whose path starts with Route (empty = any)
// and whose tenant equals Tenant (empty or "*" = any).
type Rule struct {
	Name          string `yaml:"name"`
	Route         string `yaml:"route"`
	Tenant        string `yaml:"tenant"`
	limiter.Limit `yaml:",inline"`
}

// Load reads the file at path (if non-empty), applies defaults and
// environment overrides, and validates the result. getenv is usually os.Getenv.
func Load(path string, getenv func(string) string) (*Config, error) {
	var data []byte
	if path != "" {
		var err error
		if data, err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}
	return Parse(data, getenv)
}

// Parse is Load for an in-memory document.
func Parse(data []byte, getenv func(string) string) (*Config, error) {
	c := &Config{
		Listen:          ":8080",
		LogLevel:        "info",
		ShutdownTimeout: 10 * time.Second,
		TenantHeader:    "X-Tenant-ID",
		Backend: Backend{
			Type:        "memory",
			FailureMode: limiter.FailLocal,
			Cooldown:    2 * time.Second,
			Redis:       Redis{Addr: "localhost:6379", Timeout: 50 * time.Millisecond, KeyPrefix: "rl:"},
		},
		Default: limiter.Limit{Algorithm: limiter.TokenBucket, Rate: 100, Period: time.Second},
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	// io.EOF means an empty document, which keeps the defaults.
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := c.applyEnv(getenv); err != nil {
		return nil, err
	}
	return c, c.Validate()
}

func (c *Config) applyEnv(getenv func(string) string) error {
	set := func(name string, dst *string) {
		if v := getenv(name); v != "" {
			*dst = v
		}
	}
	set("EDGE_LIMITER_LISTEN", &c.Listen)
	set("EDGE_LIMITER_LOG_LEVEL", &c.LogLevel)
	set("EDGE_LIMITER_BACKEND", &c.Backend.Type)
	set("EDGE_LIMITER_REDIS_ADDR", &c.Backend.Redis.Addr)
	set("EDGE_LIMITER_REDIS_PASSWORD", &c.Backend.Redis.Password)
	if v := getenv("EDGE_LIMITER_FAILURE_MODE"); v != "" {
		c.Backend.FailureMode = limiter.FailureMode(v)
	}
	if v := getenv("EDGE_LIMITER_REDIS_TLS"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("config: EDGE_LIMITER_REDIS_TLS: %w", err)
		}
		c.Backend.Redis.TLS = b
	}
	return nil
}

// Validate checks every limit and backend setting.
func (c *Config) Validate() error {
	var errs []error
	if c.Backend.Type != "memory" && c.Backend.Type != "redis" {
		errs = append(errs, fmt.Errorf("backend.type %q: want memory or redis", c.Backend.Type))
	}
	if _, err := limiter.ParseFailureMode(string(c.Backend.FailureMode)); err != nil {
		errs = append(errs, err)
	}
	if err := c.Default.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("default: %w", err))
	}
	seen := map[string]bool{"default": true}
	for i, r := range c.Rules {
		if r.Name == "" || seen[r.Name] {
			errs = append(errs, fmt.Errorf("rules[%d]: name %q must be unique and non-empty", i, r.Name))
		}
		seen[r.Name] = true
		if err := r.Limit.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("rules[%d] %s: %w", i, r.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Resolve returns the first rule matching route and tenant, or the default.
func (c *Config) Resolve(route, tenant string) Rule {
	for _, r := range c.Rules {
		if (r.Route == "" || strings.HasPrefix(route, r.Route)) &&
			(r.Tenant == "" || r.Tenant == "*" || r.Tenant == tenant) {
			return r
		}
	}
	return Rule{Name: "default", Limit: c.Default}
}
