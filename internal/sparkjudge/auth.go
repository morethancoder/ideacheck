package sparkjudge

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/morethancoder/ideacheck/server"
)

// The headers the app sends. The user id is a UUID the app generates once and
// also hands RevenueCat as its appUserID, so a purchase and a check name the
// same person.
const (
	HeaderUser      = "X-App-User-Id"
	HeaderKey       = "X-App-Attest-Key"       // base64 key id, as DCAppAttestService.generateKey returned it
	HeaderAssertion = "X-App-Attest-Assertion" // base64 of DCAppAttestService.generateAssertion's result
)

var p256 = elliptic.P256()

var uuidPattern = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

func refuse(status int, code, message string) *server.Error {
	return &server.Error{Status: status, Code: code, Message: message}
}

// user reads and checks the caller's user id, and spends one of their
// requests for the minute.
func (s *Service) user(r *http.Request) (string, error) {
	user := r.Header.Get(HeaderUser)
	if !uuidPattern.MatchString(user) {
		return "", refuse(http.StatusUnauthorized, "missing_user", HeaderUser+" must be the app's user id (a UUID)")
	}
	if !s.requests.allow(user) {
		return "", refuse(http.StatusTooManyRequests, "rate_limited", "too many requests; slow down and try again in a minute")
	}
	return user, nil
}

// Auth admits a request (server.Server.Auth): the user id alone with attest
// off, else an assertion over this very request by a key the user attested.
func (s *Service) Auth(r *http.Request) (string, error) {
	user, err := s.user(r)
	if err != nil || s.Config.Attest.Mode == AttestOff {
		return user, err
	}
	keyID := r.Header.Get(HeaderKey)
	assertion, err := base64.StdEncoding.DecodeString(r.Header.Get(HeaderAssertion))
	if keyID == "" || err != nil || len(assertion) == 0 {
		return "", refuse(http.StatusUnauthorized, "attestation_required", "sign the request: "+HeaderKey+" and "+HeaderAssertion)
	}
	key, err := s.Accounts.Key(r.Context(), keyID)
	if errors.Is(err, ErrNotFound) || (err == nil && key.User != user) {
		return "", refuse(http.StatusUnauthorized, "unknown_key", "this key was never attested for this user; attest it at POST /v1/attest")
	}
	if err != nil {
		return "", err
	}
	body, err := s.body(r)
	if err != nil {
		return "", err
	}
	counter, err := s.Verifier.Assertion(assertion, key, clientData(r, body))
	if err != nil {
		s.Log.Warn("assertion refused", "error", err, "user", user)
		return "", refuse(http.StatusUnauthorized, "bad_assertion", "the request's assertion does not verify")
	}
	// Stored before the request is served, in one conditional write: of two
	// requests carrying the same assertion, only one gets through.
	if err := s.Accounts.Advance(r.Context(), key.ID, counter); errors.Is(err, ErrStale) {
		return "", refuse(http.StatusUnauthorized, "stale_assertion", "this assertion was already used or overtaken; sign the request again")
	} else if err != nil {
		return "", err
	}
	return user, nil
}

// body reads the request body for its hash and puts it back for the route.
func (s *Service) body(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, s.Config.Limits.MaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > s.Config.Limits.MaxBodyBytes {
		return nil, refuse(http.StatusRequestEntityTooLarge, "too_large", "the request body is too large")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// clientData is what the app signs for a request: the method, the path with
// its query, and the SHA-256 of the body, one per line. Binding the body means
// an assertion cannot be lifted onto a different check.
func clientData(r *http.Request, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(r.Method + "\n" + r.URL.RequestURI() + "\n" + hex.EncodeToString(sum[:]))
}

// challenge issues a one-time challenge for the attestation that follows.
func (s *Service) challenge(w http.ResponseWriter, r *http.Request) {
	user, err := s.user(r)
	if err != nil {
		server.Fail(w, http.StatusUnauthorized, err)
		return
	}
	c := make([]byte, 32)
	_, _ = rand.Read(c)
	expires := s.now().Add(s.Config.Attest.ChallengeTTL)
	if err := s.Accounts.PutChallenge(r.Context(), user, c, expires); err != nil {
		server.Fail(w, http.StatusInternalServerError, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"challenge": base64.StdEncoding.EncodeToString(c), "expires_at": expires.UTC().Format(timeFormat)})
}

type attestRequest struct {
	KeyID       string `json:"key_id"`      // base64, as generateKey returned it
	Attestation string `json:"attestation"` // base64 of attestKey's result
	Challenge   string `json:"challenge"`   // base64, as /v1/attest/challenge returned it
}

// attest verifies a new key's attestation and remembers the key for the user.
func (s *Service) attest(w http.ResponseWriter, r *http.Request) {
	user, err := s.user(r)
	if err != nil {
		server.Fail(w, http.StatusUnauthorized, err)
		return
	}
	if s.Config.Attest.Mode == AttestOff {
		server.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "attest": AttestOff})
		return
	}
	var req attestRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.Config.Limits.MaxBodyBytes)).Decode(&req); err != nil {
		server.Fail(w, http.StatusBadRequest, err)
		return
	}
	keyID, err1 := base64.StdEncoding.DecodeString(req.KeyID)
	object, err2 := base64.StdEncoding.DecodeString(req.Attestation)
	challenge, err3 := base64.StdEncoding.DecodeString(req.Challenge)
	if err := errors.Join(err1, err2, err3); err != nil || len(keyID) == 0 || len(object) == 0 {
		server.Fail(w, http.StatusBadRequest, refuse(http.StatusBadRequest, "bad_request", "key_id, attestation and challenge must be base64"))
		return
	}
	if err := s.Accounts.TakeChallenge(r.Context(), user, challenge, s.now()); err != nil {
		server.Fail(w, http.StatusUnauthorized, refuse(http.StatusUnauthorized, "bad_challenge", "the challenge is unknown, used or expired; ask for a new one"))
		return
	}
	hash := sha256.Sum256(challenge)
	got, err := s.Verifier.Attestation(object, keyID, hash[:])
	if err != nil {
		s.Log.Warn("attestation refused", "error", err, "user", user)
		server.Fail(w, http.StatusUnauthorized, refuse(http.StatusUnauthorized, "bad_attestation", "the attestation does not verify"))
		return
	}
	id := base64.StdEncoding.EncodeToString(keyID)
	key := Key{ID: id, User: user, PublicKey: got.PublicKey, Receipt: got.Receipt, Env: got.Env, CreatedAt: s.now()}
	if err := s.Accounts.AddKey(context.WithoutCancel(r.Context()), key); err != nil {
		server.Fail(w, http.StatusConflict, refuse(http.StatusConflict, "key_exists", "this key is already attested"))
		return
	}
	server.WriteJSON(w, http.StatusCreated, map[string]string{"status": "ok", "key_id": id})
}
