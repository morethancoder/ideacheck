package sparkjudge

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"math/big"
	"net/http"
	"testing"
	"time"
)

// device plays the part of an iPhone's Secure Enclave and Apple's attestation
// service, under a test root CA: it attests a key the way DCAppAttestService
// does and signs requests with it.
type device struct {
	t       *testing.T
	roots   *x509.CertPool
	caKey   *ecdsa.PrivateKey
	caCert  *x509.Certificate
	key     *ecdsa.PrivateKey
	keyID   []byte
	appID   string
	aaguid  string
	counter uint32
}

const testAppID = "ABCDE12345.com.morethancoder.sparkjudge"

func newDevice(t *testing.T) *device {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test App Attestation Root CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	d := &device{t: t, roots: roots, caKey: caKey, caCert: caCert, appID: testAppID, aaguid: "appattest\x00\x00\x00\x00\x00\x00\x00"}
	d.newKey()
	return d
}

// newKey is DCAppAttestService.generateKey: a fresh key, its id the SHA-256
// of the public point.
func (d *device) newKey() {
	d.key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pub, _ := d.key.PublicKey.Bytes()
	sum := sha256.Sum256(pub)
	d.keyID, d.counter = sum[:], 0
}

func (d *device) keyIDString() string { return base64.StdEncoding.EncodeToString(d.keyID) }

// attest is DCAppAttestService.attestKey over SHA256(challenge): an
// intermediate CA and a credential certificate whose nonce extension binds
// the authenticator data to the challenge.
func (d *device) attest(challenge []byte) []byte {
	d.t.Helper()
	interKey, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	interTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Test App Attestation CA 1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	interDER, err := x509.CreateCertificate(rand.Reader, interTmpl, d.caCert, &interKey.PublicKey, d.caKey)
	if err != nil {
		d.t.Fatal(err)
	}
	inter, _ := x509.ParseCertificate(interDER)

	pub, _ := d.key.PublicKey.Bytes()
	rp := sha256.Sum256([]byte(d.appID))
	auth := append([]byte{}, rp[:]...)
	auth = append(auth, 0x40)       // flags: attested credential data
	auth = append(auth, 0, 0, 0, 0) // counter 0
	auth = append(auth, d.aaguid...)
	auth = binary.BigEndian.AppendUint16(auth, uint16(len(d.keyID)))
	auth = append(auth, d.keyID...)
	auth = append(auth, coseKey(pub)...)

	clientDataHash := sha256.Sum256(challenge)
	nonce := sha256.Sum256(append(append([]byte{}, auth...), clientDataHash[:]...))
	octets, _ := asn1.Marshal(nonce[:])
	ext, _ := asn1.Marshal([]asn1.RawValue{{Class: asn1.ClassContextSpecific, Tag: 1, IsCompound: true, Bytes: octets}})
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "credential"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		ExtraExtensions: []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 8, 2}, Value: ext}},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, inter, &d.key.PublicKey, interKey)
	if err != nil {
		d.t.Fatal(err)
	}
	return cborMap(
		"fmt", cborText("apple-appattest"),
		"attStmt", cborMap("x5c", cborArray(cborBytes(leafDER), cborBytes(interDER)), "receipt", cborBytes([]byte("receipt"))),
		"authData", cborBytes(auth),
	)
}

// assert is DCAppAttestService.generateAssertion over SHA256(clientData).
func (d *device) assert(clientData []byte) []byte {
	d.counter++
	rp := sha256.Sum256([]byte(d.appID))
	auth := append(append([]byte{}, rp[:]...), 0)
	auth = binary.BigEndian.AppendUint32(auth, d.counter)
	cdh := sha256.Sum256(clientData)
	nonce := sha256.Sum256(append(append([]byte{}, auth...), cdh[:]...))
	digest := sha256.Sum256(nonce[:])
	sig, err := ecdsa.SignASN1(rand.Reader, d.key, digest[:])
	if err != nil {
		d.t.Fatal(err)
	}
	return cborMap("signature", cborBytes(sig), "authenticatorData", cborBytes(auth))
}

// sign adds the headers the app sends with a signed request.
func (d *device) sign(req *http.Request, user string, body []byte) {
	req.Header.Set(HeaderUser, user)
	req.Header.Set(HeaderKey, d.keyIDString())
	req.Header.Set(HeaderAssertion, base64.StdEncoding.EncodeToString(d.assert(clientData(req, body))))
}

// A small CBOR encoder, enough for the objects Apple sends.
func cborHead(major byte, n int) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 256:
		return []byte{major<<5 | 24, byte(n)}
	default:
		return binary.BigEndian.AppendUint16([]byte{major<<5 | 25}, uint16(n))
	}
}

func cborBytes(b []byte) []byte { return append(cborHead(2, len(b)), b...) }
func cborText(s string) []byte  { return append(cborHead(3, len(s)), s...) }

func cborArray(items ...[]byte) []byte {
	out := cborHead(4, len(items))
	for _, it := range items {
		out = append(out, it...)
	}
	return out
}

// cborMap takes text keys and encoded values, alternating.
func cborMap(kv ...any) []byte {
	out := cborHead(5, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		out = append(out, cborText(kv[i].(string))...)
		out = append(out, kv[i+1].([]byte)...)
	}
	return out
}

// coseKey is the EC2 P-256 COSE key in the attested credential data.
func coseKey(uncompressed []byte) []byte {
	out := []byte{0xa5, 0x01, 0x02, 0x03, 0x26, 0x20, 0x01}
	out = append(out, 0x21)
	out = append(out, cborBytes(uncompressed[1:33])...)
	out = append(out, 0x22)
	return append(out, cborBytes(uncompressed[33:65])...)
}
