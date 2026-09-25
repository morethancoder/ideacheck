package sparkjudge

import (
	"net/http"

	"github.com/morethancoder/ideacheck/server"
)

// Handler serves the check API (api) behind the service's Auth and Meter, plus
// the routes that come before a signed request is possible: the attestation
// that makes one, and RevenueCat's webhook, which carries its own secret.
func (s *Service) Handler(api *server.Server) http.Handler {
	s.Init()
	api.Auth = s.Auth
	api.Meter = s.Meter
	api.Mount = func(mux *http.ServeMux) {
		mux.HandleFunc("GET /v1/me", s.me)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/attest/challenge", s.challenge)
	mux.HandleFunc("POST /v1/attest", s.attest)
	mux.HandleFunc("POST /v1/webhooks/revenuecat", s.revenuecat)
	mux.Handle("/", api.Handler())
	return mux
}
