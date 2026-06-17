package config

import (
	"strings"
	"testing"
	"time"
)

const validYAML = `
global:
  loadBalancing: lessQueue
  requestTimeoutSecs: 600
  healthRetrySec: 5
  queuePollSec: 5
models:
  deepseek:
    urls:
      - http://model1:3000
      - http://model2:3000
  qwen:
    requestTimeoutSecs: 300
    urls:
      - http://model4:3000
clients:
  leha:
    cn: CI01929381
    concurrencyLimit:
      - model: deepseek
        maxParallel: 5
  semen:
    cn: CI01922362
    concurrencyLimit:
      - model: deepseek
        maxParallel: 1
      - model: qwen
        maxParallel: 5
tls:
  cert: /opt/certs/cert.pem
  key: /opt/certs/key.pem
  cacert: /opt/certs/cacert.pem
  verify: true
`

func TestParseValid(t *testing.T) {
	cfg, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Global.LoadBalancing != LBLessQueue {
		t.Errorf("loadBalancing = %q", cfg.Global.LoadBalancing)
	}
	if got := cfg.RequestTimeout("qwen"); got != 300*time.Second {
		t.Errorf("qwen timeout = %v, want 300s", got)
	}
	if got := cfg.RequestTimeout("deepseek"); got != 600*time.Second {
		t.Errorf("deepseek timeout = %v, want 600s (global)", got)
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
models:
  m:
    urls: [http://a:1]
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Global.LoadBalancing != DefaultLoadBalancing {
		t.Errorf("default loadBalancing = %q", cfg.Global.LoadBalancing)
	}
	if cfg.Global.RequestTimeoutSecs != DefaultRequestTimeoutSecs {
		t.Errorf("default timeout = %d", cfg.Global.RequestTimeoutSecs)
	}
	if cfg.Global.HealthRetrySec != DefaultHealthRetrySec {
		t.Errorf("default healthRetry = %d", cfg.Global.HealthRetrySec)
	}
	if cfg.Global.QueuePollSec != DefaultQueuePollSec {
		t.Errorf("default queuePoll = %d", cfg.Global.QueuePollSec)
	}
}

func TestLimit(t *testing.T) {
	cfg := mustParse(t, validYAML)
	if max, ok := cfg.Limit("CI01922362", "deepseek"); !ok || max != 1 {
		t.Errorf("semen/deepseek = %d,%v want 1,true", max, ok)
	}
	if max, ok := cfg.Limit("CI01922362", "qwen"); !ok || max != 5 {
		t.Errorf("semen/qwen = %d,%v want 5,true", max, ok)
	}
	// leha has no qwen limit -> unlimited.
	if _, ok := cfg.Limit("CI01929381", "qwen"); ok {
		t.Errorf("leha/qwen should be unlimited (ok=false)")
	}
	// Unknown CN -> unlimited.
	if _, ok := cfg.Limit("nope", "qwen"); ok {
		t.Errorf("unknown CN should be unlimited (ok=false)")
	}
}

func TestClientNameByCN(t *testing.T) {
	cfg := mustParse(t, validYAML)
	if name, ok := cfg.ClientNameByCN("CI01929381"); !ok || name != "leha" {
		t.Errorf("CN lookup = %q,%v want leha,true", name, ok)
	}
	if _, ok := cfg.ClientNameByCN("missing"); ok {
		t.Errorf("missing CN should not resolve")
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"empty urls": `
models:
  m:
    urls: []
`,
		"no models": `
global:
  loadBalancing: random
`,
		"bad loadBalancing": `
global:
  loadBalancing: bogus
models:
  m:
    urls: [http://a:1]
`,
		"unknown model in limit": `
models:
  m:
    urls: [http://a:1]
clients:
  c:
    cn: CN1
    concurrencyLimit:
      - model: nope
        maxParallel: 1
`,
		"duplicate cn": `
models:
  m:
    urls: [http://a:1]
clients:
  a:
    cn: SAME
  b:
    cn: SAME
`,
		"bad url scheme": `
models:
  m:
    urls: [ftp://a:1]
`,
		"non-positive maxParallel": `
models:
  m:
    urls: [http://a:1]
clients:
  c:
    cn: CN1
    concurrencyLimit:
      - model: m
        maxParallel: 0
`,
		"verify without cacert": `
models:
  m:
    urls: [http://a:1]
tls:
  verify: true
`,
		"cert without key": `
models:
  m:
    urls: [http://a:1]
tls:
  cert: /x
`,
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(yaml)); err == nil {
				t.Fatalf("expected validation error, got nil")
			}
		})
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	_, err := Parse([]byte(`
models:
  m:
    urls: [http://a:1]
bogusTopLevel: 1
`))
	if err == nil || !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("expected parse error for unknown field, got %v", err)
	}
}

func mustParse(t *testing.T, y string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cfg
}
