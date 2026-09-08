package network

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

func Certificate(privateDir string) (tls.Certificate, string, error) {
	path := filepath.Join(privateDir, "network.pem")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		pub, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return tls.Certificate{}, "", e
		}
		serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if e != nil {
			return tls.Certificate{}, "", e
		}
		template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "agent_room"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, e := x509.CreateCertificate(rand.Reader, template, template, pub, key)
		if e != nil {
			return tls.Certificate{}, "", e
		}
		priv, e := x509.MarshalPKCS8PrivateKey(key)
		if e != nil {
			return tls.Certificate{}, "", e
		}
		b = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})...)
		// Publish the complete keypair once; competing initializers must not rotate it.
		f, e := os.CreateTemp(privateDir, ".network-*")
		if e != nil {
			return tls.Certificate{}, "", e
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if _, e = f.Write(b); e == nil {
			e = f.Sync()
		}
		if e != nil {
			return tls.Certificate{}, "", e
		}
		if e = f.Close(); e != nil {
			return tls.Certificate{}, "", e
		}
		if e = os.Link(f.Name(), path); e != nil && !errors.Is(e, os.ErrExist) {
			return tls.Certificate{}, "", e
		}
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return tls.Certificate{}, "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return tls.Certificate{}, "", errors.New("certificate file must be private")
	}
	cert, err := tls.X509KeyPair(b, b)
	if err != nil {
		return cert, "", err
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return cert, hex.EncodeToString(sum[:]), nil
}

func PinnedTLS(fingerprint string) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13,
		// The session ID supplies an exact certificate pin in place of a public CA.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) != 1 {
				return errors.New("unexpected host certificate")
			}
			cert := cs.PeerCertificates[0]
			sum := sha256.Sum256(cert.Raw)
			if hex.EncodeToString(sum[:]) != fingerprint {
				return errors.New("host fingerprint does not match session_id")
			}
			if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
				return errors.New("host certificate expired or not yet valid")
			}
			return nil
		},
	}
}
