package certificateimport

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// leaf parses the first CERTIFICATE block of certificate_pem.
func leaf(certPEM string) (*x509.Certificate, error) {
	rest := []byte(certPEM)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, errors.New("certificate_pem holds no PEM CERTIFICATE block")
		}
		if block.Type == "CERTIFICATE" {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("certificate_pem: %w", err)
			}
			return cert, nil
		}
	}
}

// Fingerprint is the SHA-256 of the leaf's DER, lower-case hex, as the API
// reports fingerprint_sha256.
func Fingerprint(certPEM string) (string, error) {
	cert, err := leaf(certPEM)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:]), nil
}

// privateKey parses a PKCS #8, PKCS #1 or SEC 1 private key.
func privateKey(keyPEM string) (crypto.PrivateKey, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, errors.New("private_key_pem holds no PEM block")
	}
	if strings.Contains(block.Type, "ENCRYPTED") || block.Headers["Proc-Type"] != "" {
		return nil, errors.New("private_key_pem is encrypted; import the key without a passphrase")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("private_key_pem is not a PKCS #8, PKCS #1 or EC private key")
}

// publicKeyEqual reports whether a private key belongs to a public key.
func publicKeyEqual(key crypto.PrivateKey, pub crypto.PublicKey) bool {
	type equaler interface{ Equal(crypto.PublicKey) bool }
	var own crypto.PublicKey
	switch k := key.(type) {
	case *rsa.PrivateKey:
		own = &k.PublicKey
	case *ecdsa.PrivateKey:
		own = &k.PublicKey
	case ed25519.PrivateKey:
		own = k.Public()
	default:
		return false
	}
	e, ok := own.(equaler)
	return ok && e.Equal(pub)
}

// Problems checks what the backend would refuse: unparseable PEM, a key that
// is not the certificate's, an expired certificate, a malformed chain. It
// returns the attribute each problem is on.
func Problems(certPEM, chainPEM, keyPEM string, now time.Time) map[string]string {
	problems := map[string]string{}
	cert, err := leaf(certPEM)
	if err != nil {
		problems["certificate_pem"] = err.Error()
	} else if now.After(cert.NotAfter) {
		problems["certificate_pem"] = fmt.Sprintf("The certificate expired on %s; the API refuses expired certificates.", cert.NotAfter.UTC().Format(time.RFC3339))
	}
	key, err := privateKey(keyPEM)
	switch {
	case err != nil:
		problems["private_key_pem"] = err.Error()
	case cert != nil && !publicKeyEqual(key, cert.PublicKey):
		problems["private_key_pem"] = "The private key does not belong to the certificate in certificate_pem."
	}
	if strings.TrimSpace(chainPEM) != "" {
		if msg := chainProblem(chainPEM); msg != "" {
			problems["chain_pem"] = msg
		}
	}
	return problems
}

func chainProblem(chainPEM string) string {
	rest := []byte(chainPEM)
	n := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return fmt.Sprintf("chain_pem holds a %s block; it must hold only the intermediate CERTIFICATE blocks.", block.Type)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Sprintf("chain_pem certificate %d: %v", n+1, err)
		}
		n++
	}
	if n == 0 {
		return "chain_pem holds no PEM CERTIFICATE block."
	}
	return ""
}
