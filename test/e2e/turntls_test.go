package e2e

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

	"github.com/KeshavGoyal26/tessera/pkg/client"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// selfSigned writes a certificate valid for 127.0.0.1 and returns the cert and key paths.
func selfSigned(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "tessera-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// TestTURNOverTLSOnly: the server offers TURN only over TLS (no UDP or TCP listener) and the client
// keeps only the turns: URL and relay candidates, so media can only flow through TURN/TLS.
func TestTURNOverTLSOnly(t *testing.T) {
	certFile, keyFile := selfSigned(t)
	t.Setenv("SSL_CERT_FILE", certFile) // the Pion client verifies the server certificate with the system roots
	port := freePort(t)
	s := startServer(t, func(c *config.Config) {
		c.TURN = config.TURNConfig{Enabled: true, UDPPort: -1, TCPPort: -1, TLSPort: port, CertFile: certFile, KeyFile: keyFile, CredentialTTL: time.Hour}
	})
	typ := mediaVia(t, s, client.Options{SettingEngine: client.LoopbackSettingEngine(), TURNSOnly: true})
	if typ != stats.CandidateRelay {
		t.Fatalf("candidate type %q, want relay", typ)
	}
}
