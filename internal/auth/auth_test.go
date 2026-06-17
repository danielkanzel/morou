package auth

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net/http"
	"testing"

	"github.com/modelrouter/router/internal/config"
)

func cfgWith(verify bool) *config.Config {
	return &config.Config{
		Clients: map[string]config.Client{
			"leha": {CN: "CN-LEHA"},
		},
		TLS: config.TLS{Verify: verify},
	}
}

func reqWithCN(cn string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	if cn != "" {
		r.TLS = &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{{Subject: pkix.Name{CommonName: cn}}},
		}
	}
	return r
}

func TestIdentifyKnownClient(t *testing.T) {
	a := New(cfgWith(true))
	id, err := a.Identify(reqWithCN("CN-LEHA"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.Name != "leha" || id.CN != "CN-LEHA" {
		t.Fatalf("got %+v", id)
	}
}

func TestIdentifyNoCert(t *testing.T) {
	a := New(cfgWith(true))
	_, err := a.Identify(reqWithCN(""))
	if !errors.Is(err, ErrNoCertificate) {
		t.Fatalf("want ErrNoCertificate, got %v", err)
	}
}

func TestIdentifyUnknownCN(t *testing.T) {
	a := New(cfgWith(true))
	_, err := a.Identify(reqWithCN("CN-OTHER"))
	if !errors.Is(err, ErrUnknownClient) {
		t.Fatalf("want ErrUnknownClient, got %v", err)
	}
}

func TestIdentifyVerifyDisabledRejects(t *testing.T) {
	a := New(cfgWith(false))
	_, err := a.Identify(reqWithCN("CN-LEHA"))
	if !errors.Is(err, ErrNoCertificate) {
		t.Fatalf("verify=false should reject as anonymous, got %v", err)
	}
}
