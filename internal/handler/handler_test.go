package handler

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelrouter/router/internal/auth"
	"github.com/modelrouter/router/internal/balancer"
	"github.com/modelrouter/router/internal/config"
	"github.com/modelrouter/router/internal/health"
	"github.com/modelrouter/router/internal/metrics"
	"github.com/modelrouter/router/internal/proxy"
	"github.com/modelrouter/router/internal/service"
)

// buildHandler wires a Handler around an upstream test server.
func buildHandler(t *testing.T, upstream string, verify bool) *Handler {
	t.Helper()
	cfg := &config.Config{
		Global: config.Global{
			LoadBalancing:      config.LBRandom,
			RequestTimeoutSecs: 5,
			HealthRetrySec:     5,
			QueuePollSec:       5,
		},
		Models: map[string]config.Model{
			"deepseek": {URLs: []string{upstream}},
		},
		Clients: map[string]config.Client{
			"leha": {CN: "CN-LEHA", ConcurrencyLimit: []config.ConcurrencyLimit{{Model: "deepseek", MaxParallel: 5}}},
		},
		TLS: config.TLS{Verify: verify},
	}
	mon := health.NewMonitor(map[string][]string{"deepseek": {upstream}}, health.Options{})
	mon.RangeBackends(func(b *health.Backend) {
		b.SetHealthyForTest(true)
		b.SetEngineForTest(health.EngineVLLM)
	})
	m := metrics.New()
	svc := service.New(cfg, mon, balancer.New(cfg.Global.LoadBalancing))
	return New(svc, proxy.New(nil), auth.New(cfg), m, nil)
}

// requestWithCN attaches a fake TLS peer certificate carrying the given CN.
func requestWithCN(req *http.Request, cn string) {
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{
			{Subject: pkix.Name{CommonName: cn}},
		},
	}
}

func TestProxyHappyPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected upstream path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	h := buildHandler(t, upstream.URL, true)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	body := `{"model":"deepseek","messages":[]}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(body))
	requestWithCN(req, "CN-LEHA")

	resp := doViaHandler(t, h, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"ok":true`) {
		t.Fatalf("unexpected body %q", resp.Body.String())
	}
}

func TestProxyUnauthorizedNoCert(t *testing.T) {
	h := buildHandler(t, "http://127.0.0.1:0", true)
	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"deepseek"}`))
	resp := doViaHandler(t, h, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.Code)
	}
}

func TestProxyForbiddenUnknownCN(t *testing.T) {
	h := buildHandler(t, "http://127.0.0.1:0", true)
	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"deepseek"}`))
	requestWithCN(req, "CN-STRANGER")
	resp := doViaHandler(t, h, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.Code)
	}
}

func TestProxyUnknownModel(t *testing.T) {
	h := buildHandler(t, "http://127.0.0.1:0", true)
	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"ghost"}`))
	requestWithCN(req, "CN-LEHA")
	resp := doViaHandler(t, h, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.Code)
	}
}

func TestModelsListsHealthyOnly(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	h := buildHandler(t, upstream.URL, true)

	req, _ := http.NewRequest(http.MethodGet, "/v1/models", nil)
	requestWithCN(req, "CN-LEHA")
	resp := doViaHandler(t, h, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Data) != 1 || out.Data[0].ID != "deepseek" {
		t.Fatalf("unexpected models: %+v", out.Data)
	}
}

func TestLivenessOpen(t *testing.T) {
	h := buildHandler(t, "http://127.0.0.1:0", true)
	req, _ := http.NewRequest(http.MethodGet, "/health", nil)
	resp := doViaHandler(t, h, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("health status = %d", resp.Code)
	}
}

// doViaHandler runs a request through the mux directly via httptest recorder.
func doViaHandler(t *testing.T, h *Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	return rec
}
