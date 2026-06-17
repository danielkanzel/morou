// Package config loads and validates the router YAML configuration.
package config

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// LoadBalancing enumerates supported load-balancing strategies.
type LoadBalancing string

const (
	// LBRandom selects a random healthy backend.
	LBRandom LoadBalancing = "random"
	// LBRoundRobin cycles through healthy backends.
	LBRoundRobin LoadBalancing = "roundRobin"
	// LBLessQueue selects the backend with the smallest observed queue.
	LBLessQueue LoadBalancing = "lessQueue"
)

// Default values applied when the corresponding config field is omitted.
const (
	DefaultLoadBalancing      = LBRandom
	DefaultRequestTimeoutSecs = 600
	DefaultHealthRetrySec     = 5
	DefaultQueuePollSec       = 5
)

// Global holds settings shared across all models.
type Global struct {
	LoadBalancing      LoadBalancing `yaml:"loadBalancing"`
	RequestTimeoutSecs int           `yaml:"requestTimeoutSecs"`
	HealthRetrySec     int           `yaml:"healthRetrySec"`
	QueuePollSec       int           `yaml:"queuePollSec"`
}

// Model describes a logical group backed by one or more upstream URLs.
type Model struct {
	// RequestTimeoutSecs overrides Global.RequestTimeoutSecs when non-nil.
	RequestTimeoutSecs *int     `yaml:"requestTimeoutSecs"`
	URLs               []string `yaml:"urls"`
}

// ConcurrencyLimit binds a per-model parallelism cap to a client.
type ConcurrencyLimit struct {
	Model       string `yaml:"model"`
	MaxParallel int    `yaml:"maxParallel"`
}

// Client describes an API consumer identified by a TLS certificate CN.
type Client struct {
	CN               string             `yaml:"cn"`
	ConcurrencyLimit []ConcurrencyLimit `yaml:"concurrencyLimit"`
}

// TLS holds the server TLS / mTLS configuration.
type TLS struct {
	Cert   string `yaml:"cert"`
	Key    string `yaml:"key"`
	CACert string `yaml:"cacert"`
	Verify bool   `yaml:"verify"`
}

// Config is the top-level configuration document.
type Config struct {
	Global  Global            `yaml:"global"`
	Models  map[string]Model  `yaml:"models"`
	Clients map[string]Client `yaml:"clients"`
	TLS     TLS               `yaml:"tls"`
}

// RequestTimeout returns the effective request timeout for the named model,
// honoring the model-level override over the global default.
func (c *Config) RequestTimeout(model string) time.Duration {
	secs := c.Global.RequestTimeoutSecs
	if m, ok := c.Models[model]; ok && m.RequestTimeoutSecs != nil {
		secs = *m.RequestTimeoutSecs
	}
	return time.Duration(secs) * time.Second
}

// HealthRetry returns the health polling interval.
func (c *Config) HealthRetry() time.Duration {
	return time.Duration(c.Global.HealthRetrySec) * time.Second
}

// QueuePoll returns the queue-depth polling interval.
func (c *Config) QueuePoll() time.Duration {
	return time.Duration(c.Global.QueuePollSec) * time.Second
}

// Limit returns the configured maxParallel for (clientCN, model). The second
// return value is false when the client has no explicit limit for that model,
// which means unlimited access.
func (c *Config) Limit(cn, model string) (int, bool) {
	for _, cl := range c.Clients {
		if cl.CN != cn {
			continue
		}
		for _, l := range cl.ConcurrencyLimit {
			if l.Model == model {
				return l.MaxParallel, true
			}
		}
		return 0, false
	}
	return 0, false
}

// ClientNameByCN maps a certificate CN to the logical client name. The bool is
// false when no client matches.
func (c *Config) ClientNameByCN(cn string) (string, bool) {
	for name, cl := range c.Clients {
		if cl.CN == cn {
			return name, true
		}
	}
	return "", false
}

// Load reads, parses, applies defaults to, and validates a config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	return Parse(raw)
}

// Parse parses config bytes, applies defaults and validates the result.
func Parse(raw []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Global.LoadBalancing == "" {
		c.Global.LoadBalancing = DefaultLoadBalancing
	}
	if c.Global.RequestTimeoutSecs == 0 {
		c.Global.RequestTimeoutSecs = DefaultRequestTimeoutSecs
	}
	if c.Global.HealthRetrySec == 0 {
		c.Global.HealthRetrySec = DefaultHealthRetrySec
	}
	if c.Global.QueuePollSec == 0 {
		c.Global.QueuePollSec = DefaultQueuePollSec
	}
}

// Validate checks structural and referential integrity of the config.
//
// Note: duplicate model/client keys are rejected by the YAML decoder
// (KnownFields cannot catch this, but a YAML map with duplicate keys is a
// parse error in yaml.v3), so here we focus on cross-field rules. Duplicate
// CNs across clients are validated explicitly.
func (c *Config) Validate() error {
	switch c.Global.LoadBalancing {
	case LBRandom, LBRoundRobin, LBLessQueue:
	default:
		return fmt.Errorf("invalid loadBalancing %q: must be one of random|roundRobin|lessQueue", c.Global.LoadBalancing)
	}

	if c.Global.RequestTimeoutSecs <= 0 {
		return fmt.Errorf("global.requestTimeoutSecs must be positive")
	}
	if c.Global.HealthRetrySec <= 0 {
		return fmt.Errorf("global.healthRetrySec must be positive")
	}
	if c.Global.QueuePollSec <= 0 {
		return fmt.Errorf("global.queuePollSec must be positive")
	}

	if len(c.Models) == 0 {
		return fmt.Errorf("no models configured")
	}

	for name, m := range c.Models {
		if len(m.URLs) == 0 {
			return fmt.Errorf("model %q: urls must not be empty", name)
		}
		for _, u := range m.URLs {
			parsed, err := url.Parse(u)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return fmt.Errorf("model %q: invalid url %q", name, u)
			}
			if parsed.Scheme != "http" && parsed.Scheme != "https" {
				return fmt.Errorf("model %q: url %q must use http or https", name, u)
			}
		}
		if m.RequestTimeoutSecs != nil && *m.RequestTimeoutSecs <= 0 {
			return fmt.Errorf("model %q: requestTimeoutSecs must be positive", name)
		}
	}

	seenCN := make(map[string]string, len(c.Clients))
	for name, cl := range c.Clients {
		if cl.CN == "" {
			return fmt.Errorf("client %q: cn must not be empty", name)
		}
		if prev, ok := seenCN[cl.CN]; ok {
			return fmt.Errorf("duplicate cn %q used by clients %q and %q", cl.CN, prev, name)
		}
		seenCN[cl.CN] = name

		seenModel := make(map[string]struct{}, len(cl.ConcurrencyLimit))
		for _, l := range cl.ConcurrencyLimit {
			if _, ok := c.Models[l.Model]; !ok {
				return fmt.Errorf("client %q: concurrencyLimit references unknown model %q", name, l.Model)
			}
			if _, dup := seenModel[l.Model]; dup {
				return fmt.Errorf("client %q: duplicate concurrencyLimit for model %q", name, l.Model)
			}
			seenModel[l.Model] = struct{}{}
			if l.MaxParallel <= 0 {
				return fmt.Errorf("client %q: maxParallel for model %q must be positive", name, l.Model)
			}
		}
	}

	if c.TLS.Verify {
		if c.TLS.CACert == "" {
			return fmt.Errorf("tls.verify is true but tls.cacert is empty")
		}
	}
	if (c.TLS.Cert == "") != (c.TLS.Key == "") {
		return fmt.Errorf("tls.cert and tls.key must be set together")
	}

	return nil
}
