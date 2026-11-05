// Package tlsgen creates certificates for the federation listener: either a
// self-signed one for local development, or one signed by a supplied CA.
// Deployments that are reachable by other homeservers should use a publicly
// trusted certificate and never this package.
package tlsgen

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Validity returns the notBefore/notAfter pair used for generated certificates.
func Validity(now time.Time) (time.Time, time.Time) {
	return now.Add(-time.Hour), now.Add(390 * 24 * time.Hour)
}

// Ensure writes a self-signed certificate and key for serverName when they do not
// already exist. Existing files are left untouched.
func Ensure(certPath, keyPath, serverName string, notBefore, notAfter time.Time) error {
	if exists, err := pairExists(certPath, keyPath); err != nil || exists {
		return err
	}
	key, err := newKey()
	if err != nil {
		return err
	}
	template, err := certificateTemplate(serverName, notBefore, notAfter)
	if err != nil {
		return err
	}
	// A self-signed certificate is its own issuer.
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, key.public(), key.signer())
	if err != nil {
		return fmt.Errorf("tlsgen: cannot create self-signed certificate: %w", err)
	}
	return writePair(certPath, keyPath, der, key)
}

// EnsureSignedByCA writes a certificate for serverName signed by the CA in
// caCertPath/caKeyPath when the certificate does not already exist. The CA
// certificate and key must already be on disk.
func EnsureSignedByCA(certPath, keyPath, serverName, caCertPath, caKeyPath string, notBefore, notAfter time.Time) error {
	if exists, err := pairExists(certPath, keyPath); err != nil || exists {
		return err
	}
	caCert, caKey, err := loadCA(caCertPath, caKeyPath)
	if err != nil {
		return err
	}
	key, err := newKey()
	if err != nil {
		return err
	}
	template, err := certificateTemplate(serverName, notBefore, notAfter)
	if err != nil {
		return err
	}
	template.BasicConstraintsValid = false
	template.IsCA = false
	template.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment

	der, err := x509.CreateCertificate(rand.Reader, &template, caCert, key.public(), caKey)
	if err != nil {
		return fmt.Errorf("tlsgen: cannot create CA-signed certificate: %w", err)
	}
	return writePair(certPath, keyPath, der, key)
}

// loadCA reads a CA certificate and its private key.
func loadCA(certPath, keyPath string) (*x509.Certificate, any, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("tlsgen: cannot read CA certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("tlsgen: cannot read CA key: %w", err)
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, errors.New("tlsgen: CA certificate is not PEM encoded")
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("tlsgen: cannot parse CA certificate: %w", err)
	}
	if !caCert.IsCA {
		return nil, nil, errors.New("tlsgen: the supplied certificate is not a CA certificate")
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, errors.New("tlsgen: CA key is not PEM encoded")
	}
	key, err := parsePrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("tlsgen: cannot parse CA key: %w", err)
	}
	return caCert, key, nil
}

// parsePrivateKey accepts the PKCS#8, SEC 1 and PKCS#1 encodings that CA
// generation tools produce.
func parsePrivateKey(der []byte) (any, error) {
	if key, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(der); err == nil {
		return key, nil
	}
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		return nil, errors.New("tlsgen: unsupported private key encoding")
	}
	return key, nil
}

// keyPair bundles a private key with a accessor for its public half.
type keyPair struct {
	ec  *ecdsa.PrivateKey
	rsa *rsa.PrivateKey
}

func (k *keyPair) public() any {
	if k.ec != nil {
		return &k.ec.PublicKey
	}
	return &k.rsa.PublicKey
}

// signer returns the private half, which x509.CreateCertificate requires.
func (k *keyPair) signer() any {
	if k.ec != nil {
		return k.ec
	}
	return k.rsa
}

func newKey() (*keyPair, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("tlsgen: cannot generate key: %w", err)
	}
	return &keyPair{ec: key}, nil
}

func certificateTemplate(serverName string, notBefore, notAfter time.Time) (x509.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return x509.Certificate{}, fmt.Errorf("tlsgen: cannot generate serial: %w", err)
	}
	host, _, serr := net.SplitHostPort(serverName)
	if serr != nil {
		host = serverName
	}
	return x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host, Organization: []string{"lattice"}},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              dnsNames(host),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}, nil
}

// writePair writes the certificate and private key with restrictive permissions.
func writePair(certPath, keyPath string, der []byte, key *keyPair) error {
	for _, path := range []string{certPath, keyPath} {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return fmt.Errorf("tlsgen: cannot create certificate directory: %w", err)
			}
		}
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o640); err != nil {
		return fmt.Errorf("tlsgen: cannot write certificate: %w", err)
	}
	var keyDER []byte
	var err error
	if key.ec != nil {
		keyDER, err = x509.MarshalECPrivateKey(key.ec)
	} else {
		keyDER, err = x509.MarshalPKCS8PrivateKey(key.rsa)
	}
	if err != nil {
		return fmt.Errorf("tlsgen: cannot marshal key: %w", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return fmt.Errorf("tlsgen: cannot write key: %w", err)
	}
	return nil
}

// pairExists reports whether both files are present.
func pairExists(certPath, keyPath string) (bool, error) {
	if certPath == "" || keyPath == "" {
		return false, errors.New("tlsgen: certificate and key paths are required")
	}
	_, cerr := os.Stat(certPath)
	_, kerr := os.Stat(keyPath)
	if errors.Is(cerr, os.ErrNotExist) || errors.Is(kerr, os.ErrNotExist) {
		return false, nil
	}
	if cerr != nil {
		return false, fmt.Errorf("tlsgen: cannot stat certificate: %w", cerr)
	}
	if kerr != nil {
		return false, fmt.Errorf("tlsgen: cannot stat key: %w", kerr)
	}
	return true, nil
}

// dnsNames returns the DNS names the federation certificate should carry: the
// server name plus loopback names for local testing.
func dnsNames(host string) []string {
	if net.ParseIP(host) != nil {
		return []string{"localhost"}
	}
	return []string{host, "localhost"}
}
