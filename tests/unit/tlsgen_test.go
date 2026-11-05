package unit

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/tlsgen"
)

// TestTLSCertificatesEnsure checks that a self-signed pair is created once and
// left alone afterwards.
func TestTLSCertificatesEnsure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls", "hs1.crt")
	keyPath := filepath.Join(dir, "tls", "hs1.key")

	notBefore, notAfter := tlsgen.Validity(time.Now())
	if err := tlsgen.Ensure(certPath, keyPath, "hs1", notBefore, notAfter); err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}
	first, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("certificate not written: %v", err)
	}
	if err := tlsgen.Ensure(certPath, keyPath, "hs1", notBefore, notAfter); err != nil {
		t.Fatalf("second Ensure failed: %v", err)
	}
	second, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("certificate disappeared: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("Ensure regenerated an existing certificate")
	}

	block, _ := pem.Decode(first)
	if block == nil {
		t.Fatalf("certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("cannot parse certificate: %v", err)
	}
	if cert.Subject.CommonName != "hs1" {
		t.Errorf("common name = %q", cert.Subject.CommonName)
	}
	if !contains(cert.DNSNames, "hs1") {
		t.Errorf("DNS names %v must include the server name", cert.DNSNames)
	}
	if !containsIP(cert.IPAddresses, net.ParseIP("127.0.0.1")) {
		t.Errorf("the certificate must cover 127.0.0.1 for local runs: %v", cert.IPAddresses)
	}
	// X.509 encodes times with one-second granularity, so allow that much slack.
	if cert.NotAfter.Before(notAfter.Add(-2 * time.Second)) {
		t.Errorf("certificate expires earlier than requested: %v vs %v", cert.NotAfter, notAfter)
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key not written: %v", err)
	}
	if info.Mode().Perm()&0o177 != 0 {
		t.Errorf("key file permissions are too open: %v", info.Mode())
	}
}

// TestTLSCertificatesSignedByCA checks the CA-signed path used by the Complement
// test harness: the leaf must verify against the CA chain.
func TestTLSCertificatesSignedByCA(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	caCertPath := filepath.Join(dir, "ca.crt")
	caKeyPath := filepath.Join(dir, "ca.key")
	writeTestCA(t, caCertPath, caKeyPath)

	certPath := filepath.Join(dir, "tls", "hs1.crt")
	keyPath := filepath.Join(dir, "tls", "hs1.key")
	notBefore, notAfter := tlsgen.Validity(time.Now())
	if err := tlsgen.EnsureSignedByCA(certPath, keyPath, "hs1", caCertPath, caKeyPath, notBefore, notAfter); err != nil {
		t.Fatalf("EnsureSignedByCA failed: %v", err)
	}

	leafPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("certificate not written: %v", err)
	}
	block, _ := pem.Decode(leafPEM)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("cannot parse leaf: %v", err)
	}
	if leaf.IsCA {
		t.Errorf("a leaf certificate must not be a CA")
	}

	pool := x509.NewCertPool()
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		t.Fatalf("cannot read CA: %v", err)
	}
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatalf("CA not added to the pool")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		DNSName:   "hs1",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("the leaf does not verify against the CA: %v", err)
	}

	// A second call must not overwrite the existing leaf.
	before, _ := os.ReadFile(certPath)
	if err := tlsgen.EnsureSignedByCA(certPath, keyPath, "hs1", caCertPath, caKeyPath, notBefore, notAfter); err != nil {
		t.Fatalf("second EnsureSignedByCA failed: %v", err)
	}
	after, _ := os.ReadFile(certPath)
	if string(before) != string(after) {
		t.Errorf("an existing leaf was replaced")
	}
}

// TestTLSCertificatesRejectsBadCA checks the failure modes: a missing CA or a
// non-CA certificate must be reported, not silently ignored.
func TestTLSCertificatesRejectsBadCA(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	err := tlsgen.EnsureSignedByCA(filepath.Join(dir, "a.crt"), filepath.Join(dir, "a.key"), "hs1",
		filepath.Join(dir, "absent.crt"), filepath.Join(dir, "absent.key"), time.Now(), time.Now().Add(time.Hour))
	if err == nil {
		t.Errorf("expected an error for a missing CA")
	}
	if err := tlsgen.Ensure("", "", "hs1", time.Now(), time.Now().Add(time.Hour)); err == nil {
		t.Errorf("expected an error for empty paths")
	}
}

// writeTestCA writes a throwaway CA into the test directory.
func writeTestCA(t *testing.T, certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("cannot generate CA key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "complement-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cannot create CA certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("cannot marshal CA key: %v", err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("cannot write CA certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("cannot write CA key: %v", err)
	}
}

// containsIP compares IPs by value, since net.IP is a slice.
func containsIP(addresses []net.IP, want net.IP) bool {
	for _, address := range addresses {
		if address.Equal(want) {
			return true
		}
	}
	return false
}

func contains[T comparable](slice []T, want T) bool {
	for _, v := range slice {
		if v == want {
			return true
		}
	}
	return false
}
