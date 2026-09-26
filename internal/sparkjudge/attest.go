package sparkjudge

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"

	attest "github.com/takimoto3/app-attest"
)

// AppleRootCA is Apple's App Attestation Root CA, from
// https://www.apple.com/certificateauthority/Apple_App_Attestation_Root_CA.pem
// (SHA-256 fingerprint 1CB9823BA28BA6AD2D33A006941DE2AE4F513EF1D4E831B9F7E0FA7B6242C932,
// valid until 2045-03-15; a test pins the fingerprint). It is public.
const AppleRootCA = `-----BEGIN CERTIFICATE-----
MIICITCCAaegAwIBAgIQC/O+DvHN0uD7jG5yH2IXmDAKBggqhkjOPQQDAzBSMSYw
JAYDVQQDDB1BcHBsZSBBcHAgQXR0ZXN0YXRpb24gUm9vdCBDQTETMBEGA1UECgwK
QXBwbGUgSW5jLjETMBEGA1UECAwKQ2FsaWZvcm5pYTAeFw0yMDAzMTgxODMyNTNa
Fw00NTAzMTUwMDAwMDBaMFIxJjAkBgNVBAMMHUFwcGxlIEFwcCBBdHRlc3RhdGlv
biBSb290IENBMRMwEQYDVQQKDApBcHBsZSBJbmMuMRMwEQYDVQQIDApDYWxpZm9y
bmlhMHYwEAYHKoZIzj0CAQYFK4EEACIDYgAERTHhmLW07ATaFQIEVwTtT4dyctdh
NbJhFs/Ii2FdCgAHGbpphY3+d8qjuDngIN3WVhQUBHAoMeQ/cLiP1sOUtgjqK9au
Yen1mMEvRq9Sk3Jm5X8U62H+xTD3FE9TgS41o0IwQDAPBgNVHRMBAf8EBTADAQH/
MB0GA1UdDgQWBBSskRBTM72+aEH/pwyp5frq5eWKoTAOBgNVHQ8BAf8EBAMCAQYw
CgYIKoZIzj0EAwMDaAAwZQIwQgFGnByvsiVbpTKwSga0kP0e8EeDS4+sQmTvb7vn
53O5+FRXgeLhpJ06ysC5PrOyAjEAp5U4xDgEgllF7En3VcE3iexZZtKeYnpqtijV
oyFraWVIyd/dganmrduC1bmTBGwD
-----END CERTIFICATE-----
`

// AppleRoots is a pool holding AppleRootCA alone.
func AppleRoots() *x509.CertPool {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(AppleRootCA)) {
		panic("sparkjudge: the embedded Apple root certificate does not parse")
	}
	return pool
}

// rootFingerprint is the SHA-256 of the root's DER, as Apple publishes it.
func rootFingerprint(pemText string) string {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return ""
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])
}

// Verifier checks App Attest attestations and assertions for one App ID,
// following Apple's "Validating apps that connect to your server"
// (developer.apple.com/documentation/devicecheck/validating-apps-that-connect-to-your-server).
// The steps themselves are github.com/takimoto3/app-attest's, read before
// choosing it: the x5c chain to Roots, nonce = SHA256(authData ||
// clientDataHash) against the credential certificate's 1.2.840.113635.100.8.2
// extension, key id = SHA256(public key), RP ID hash = SHA256(App ID),
// counter 0 and the aaguid; for an assertion, the signature over
// SHA256(authData || SHA256(clientData)) and a counter above the stored one.
type Verifier struct {
	Roots       *x509.CertPool // AppleRoots(); a test brings its own
	AppID       string         // teamID.bundleID
	Development bool           // accept keys attested by development builds
}

// Attested is a key the app proved came from a genuine copy of it.
type Attested struct {
	PublicKey []byte // uncompressed P-256 point
	Receipt   []byte
	Env       string
}

// Attestation verifies the object DCAppAttestService.attestKey returned for
// keyID, made over clientDataHash = SHA256(the challenge this server issued).
func (v Verifier) Attestation(object, keyID, clientDataHash []byte) (Attested, error) {
	var obj attest.AttestationObject
	if err := obj.UnmarshalCBOR(object); err != nil {
		return Attested{}, fmt.Errorf("attestation: %w", err)
	}
	res, err := attest.NewAttestationService(v.Roots, v.AppID).Verify(&obj, clientDataHash, keyID)
	if err != nil {
		return Attested{}, fmt.Errorf("attestation: %w", err)
	}
	env := "production"
	if res.Environment != attest.Production {
		if !v.Development {
			return Attested{}, fmt.Errorf("attestation: a development build's key; this server accepts production keys only")
		}
		env = "development"
	}
	pub, err := res.PublicKey.Bytes()
	if err != nil {
		return Attested{}, fmt.Errorf("attestation: %w", err)
	}
	return Attested{PublicKey: pub, Receipt: res.Receipt, Env: env}, nil
}

// Assertion verifies an assertion made with key over clientData and returns
// its counter, which must be above key.Counter and stored before the request
// is served.
func (v Verifier) Assertion(assertion []byte, key Key, clientData []byte) (uint32, error) {
	var obj attest.AssertionObject
	if err := obj.UnmarshalCBOR(assertion); err != nil {
		return 0, fmt.Errorf("assertion: %w", err)
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(p256, key.PublicKey)
	if err != nil {
		return 0, fmt.Errorf("stored key: %w", err)
	}
	// The request itself is the client data (see clientData), so no separate
	// challenge is embedded: the counter makes each assertion single-use. It
	// is compared with the stored one by the caller, in the same write that
	// stores it (Accounts.Advance), so a replay is told apart from a forgery.
	svc := attest.AssertionService{AppID: v.AppID, PublicKey: pub}
	counter, err := svc.Verify(&obj, "", clientData)
	if err != nil {
		return 0, fmt.Errorf("assertion: %w", err)
	}
	return counter, nil
}
