// Package testpki generates an enterprise-shaped test PKI (root CA ->
// intermediate CA -> leaves) for mTLS tests and the essim simulator.
// Never use it for anything but tests and demos.
package testpki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// PKI lists the generated files (all PEM).
type PKI struct {
	Dir        string
	RootCert   string // root CA certificate
	CAChain    string // intermediate + root
	ServerCert string // server leaf + intermediate (full chain)
	ServerKey  string
	ClientCert string // client leaf (CN=estopo) + intermediate
	ClientKey  string
}

// Generate writes a fresh PKI into dir. The server certificate is valid for
// hosts (DNS names or IPs); localhost and 127.0.0.1 are always included.
func Generate(dir string, hosts ...string) (*PKI, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	now := time.Now().Add(-time.Hour)
	subject := func(cn string) pkix.Name {
		return pkix.Name{Organization: []string{"ES.Topo test"}, OrganizationalUnit: []string{"run"}, CommonName: cn}
	}

	rootKey, root, err := issue(&x509.Certificate{
		Subject: subject("Test Root CA"), NotBefore: now, NotAfter: now.AddDate(10, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}, nil, nil)
	if err != nil {
		return nil, err
	}
	interKey, inter, err := issue(&x509.Certificate{
		Subject: subject("Test Intermediate CA"), NotBefore: now, NotAfter: now.AddDate(10, 0, 0),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}, root, rootKey)
	if err != nil {
		return nil, err
	}

	server := &x509.Certificate{
		Subject: subject("es-node"), NotBefore: now, NotAfter: now.AddDate(10, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:    []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			server.IPAddresses = append(server.IPAddresses, ip)
		} else {
			server.DNSNames = append(server.DNSNames, h)
		}
	}
	serverKey, serverCert, err := issue(server, inter, interKey)
	if err != nil {
		return nil, err
	}
	clientKey, clientCert, err := issue(&x509.Certificate{
		Subject: subject("estopo"), NotBefore: now, NotAfter: now.AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, inter, interKey)
	if err != nil {
		return nil, err
	}

	p := &PKI{
		Dir:        dir,
		RootCert:   filepath.Join(dir, "root.crt"),
		CAChain:    filepath.Join(dir, "ca-chain.crt"),
		ServerCert: filepath.Join(dir, "server.crt"),
		ServerKey:  filepath.Join(dir, "server.key"),
		ClientCert: filepath.Join(dir, "client.crt"),
		ClientKey:  filepath.Join(dir, "client.key"),
	}
	files := []struct {
		path  string
		certs []*x509.Certificate
		key   *ecdsa.PrivateKey
	}{
		{p.RootCert, []*x509.Certificate{root}, nil},
		{p.CAChain, []*x509.Certificate{inter, root}, nil},
		{p.ServerCert, []*x509.Certificate{serverCert, inter}, nil},
		{p.ServerKey, nil, serverKey},
		{p.ClientCert, []*x509.Certificate{clientCert, inter}, nil},
		{p.ClientKey, nil, clientKey},
	}
	for _, f := range files {
		var out []byte
		for _, c := range f.certs {
			out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})...)
		}
		if f.key != nil {
			der, err := x509.MarshalPKCS8PrivateKey(f.key)
			if err != nil {
				return nil, err
			}
			out = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		}
		if err := os.WriteFile(f.path, out, 0o600); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func issue(tmpl, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*ecdsa.PrivateKey, *x509.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return nil, nil, err
	}
	tmpl.SerialNumber = serial
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		return nil, nil, fmt.Errorf("issue %s: %w", tmpl.Subject.CommonName, err)
	}
	cert, err := x509.ParseCertificate(der)
	return key, cert, err
}
