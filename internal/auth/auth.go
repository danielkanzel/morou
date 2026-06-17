// Package auth identifies API consumers from their client TLS certificate.
package auth

import (
	"crypto/tls"
	"errors"
	"net/http"

	"github.com/modelrouter/router/internal/config"
)

// Errors returned by Identify, mapped to HTTP status codes by the handler.
var (
	// ErrNoCertificate indicates a missing/invalid client certificate (-> 401).
	ErrNoCertificate = errors.New("no valid client certificate presented")
	// ErrUnknownClient indicates the CN is not configured (-> 403).
	ErrUnknownClient = errors.New("client CN not authorized")
)

// Identity is the result of identifying a request's consumer.
type Identity struct {
	// Name is the logical client name from config.
	Name string
	// CN is the certificate common name.
	CN string
}

// Authenticator resolves request identities against the configured clients.
type Authenticator struct {
	cfg    *config.Config
	verify bool
}

// New builds an Authenticator. verify mirrors tls.verify: when false, mTLS is
// not enforced and every request is anonymous (and therefore rejected, since
// limits are bound to CNs).
func New(cfg *config.Config) *Authenticator {
	return &Authenticator{cfg: cfg, verify: cfg.TLS.Verify}
}

// Identify extracts the client identity from the request's TLS state.
func (a *Authenticator) Identify(r *http.Request) (Identity, error) {
	if !a.verify {
		// Without mTLS we cannot bind a request to a CN.
		return Identity{}, ErrNoCertificate
	}
	cn, err := clientCN(r.TLS)
	if err != nil {
		return Identity{}, err
	}
	name, ok := a.cfg.ClientNameByCN(cn)
	if !ok {
		return Identity{CN: cn}, ErrUnknownClient
	}
	return Identity{Name: name, CN: cn}, nil
}

func clientCN(state *tls.ConnectionState) (string, error) {
	if state == nil || len(state.PeerCertificates) == 0 {
		return "", ErrNoCertificate
	}
	cn := state.PeerCertificates[0].Subject.CommonName
	if cn == "" {
		return "", ErrNoCertificate
	}
	return cn, nil
}
