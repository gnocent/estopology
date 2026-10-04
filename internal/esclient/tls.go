package esclient

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// buildTLSConfig assembles the TLS configuration of a cluster connection
// (SPEC §5.6): TLS 1.2+, CA bundle from a directory or file (global, or
// overridden per cluster), optional system pool, optional client certificate.
func buildTLSConfig(cfg Config) (*tls.Config, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS12}

	var pool *x509.CertPool
	if cfg.UseSystemCAs {
		sys, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("system CA pool: %w", err)
		}
		pool = sys
	} else {
		pool = x509.NewCertPool()
	}
	caPath := cfg.CAPath
	if caPath != "" {
		n, err := loadCAs(pool, caPath)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("no CA certificate found in %s", caPath)
		}
	}
	if caPath != "" || cfg.UseSystemCAs {
		tc.RootCAs = pool
	}

	if cfg.Auth == AuthCert {
		cert, err := loadKeyPair(cfg.CertFile, cfg.KeyFile, cfg.KeyPassphraseFile)
		if err != nil {
			return nil, err
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	return tc, nil
}

// loadCAs adds every certificate of path to pool. path is a PEM file or a
// directory whose *.pem, *.crt and *.cer files are all loaded (enterprise root
// and intermediates). It returns the number of certificates added.
func loadCAs(pool *x509.CertPool, path string) (int, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("CA path: %w", err)
	}
	files := []string{path}
	if st.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return 0, fmt.Errorf("CA directory: %w", err)
		}
		files = files[:0]
		for _, e := range entries {
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if !e.IsDir() && (ext == ".pem" || ext == ".crt" || ext == ".cer") {
				files = append(files, filepath.Join(path, e.Name()))
			}
		}
		sort.Strings(files)
	}
	n := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return n, fmt.Errorf("CA file: %w", err)
		}
		for {
			var block *pem.Block
			block, data = pem.Decode(data)
			if block == nil {
				break
			}
			if block.Type != "CERTIFICATE" {
				continue
			}
			c, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return n, fmt.Errorf("CA file %s: %w", f, err)
			}
			pool.AddCert(c)
			n++
		}
	}
	return n, nil
}

// loadKeyPair loads a client certificate (full chain accepted) and its key.
// The key may be encrypted (PKCS#8 PBES2, OpenSSL's default); its passphrase
// is then read from passphraseFile.
func loadKeyPair(certFile, keyFile, passphraseFile string) (tls.Certificate, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("client certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("client key: %w", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return tls.Certificate{}, fmt.Errorf("client key %s: no PEM block", keyFile)
	}
	if block.Type == "ENCRYPTED PRIVATE KEY" {
		if passphraseFile == "" {
			return tls.Certificate{}, fmt.Errorf("client key %s is encrypted but no passphrase file is configured", keyFile)
		}
		pass, err := os.ReadFile(passphraseFile)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("key passphrase: %w", err)
		}
		der, err := decryptPKCS8(block.Bytes, []byte(strings.TrimRight(string(pass), "\r\n")))
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("client key %s: %w", keyFile, err)
		}
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("client certificate/key: %w", err)
	}
	return cert, nil
}

var (
	oidPBES2      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 13}
	oidPBKDF2     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 12}
	oidHMACSHA1   = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 7}
	oidHMACSHA256 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 9}
	oidHMACSHA512 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 11}
	oidAES128CBC  = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 2}
	oidAES192CBC  = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 22}
	oidAES256CBC  = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
)

type encryptedPrivateKeyInfo struct {
	Algo          pkix.AlgorithmIdentifier
	EncryptedData []byte
}

type pbes2Params struct {
	KeyDerivationFunc pkix.AlgorithmIdentifier
	EncryptionScheme  pkix.AlgorithmIdentifier
}

type pbkdf2Params struct {
	Salt           []byte
	IterationCount int
	KeyLength      int                      `asn1:"optional"`
	PRF            pkix.AlgorithmIdentifier `asn1:"optional"`
}

// decryptPKCS8 decrypts a PKCS#8 EncryptedPrivateKeyInfo using PBES2 with
// PBKDF2 and AES-CBC, which covers keys produced by OpenSSL 1.1+ and 3.x.
func decryptPKCS8(der, passphrase []byte) ([]byte, error) {
	var info encryptedPrivateKeyInfo
	if _, err := asn1.Unmarshal(der, &info); err != nil {
		return nil, fmt.Errorf("encrypted key: %w", err)
	}
	if !info.Algo.Algorithm.Equal(oidPBES2) {
		return nil, fmt.Errorf("unsupported key encryption %v (only PBES2 is supported; re-encrypt with openssl pkcs8 -topk8 -v2 aes256)", info.Algo.Algorithm)
	}
	var params pbes2Params
	if _, err := asn1.Unmarshal(info.Algo.Parameters.FullBytes, &params); err != nil {
		return nil, fmt.Errorf("PBES2 parameters: %w", err)
	}
	if !params.KeyDerivationFunc.Algorithm.Equal(oidPBKDF2) {
		return nil, errors.New("unsupported key derivation (only PBKDF2 is supported)")
	}
	var kdf pbkdf2Params
	if _, err := asn1.Unmarshal(params.KeyDerivationFunc.Parameters.FullBytes, &kdf); err != nil {
		return nil, fmt.Errorf("PBKDF2 parameters: %w", err)
	}
	var h func() hash.Hash
	switch {
	case kdf.PRF.Algorithm == nil, kdf.PRF.Algorithm.Equal(oidHMACSHA1):
		h = sha1.New
	case kdf.PRF.Algorithm.Equal(oidHMACSHA256):
		h = sha256.New
	case kdf.PRF.Algorithm.Equal(oidHMACSHA512):
		h = sha512.New
	default:
		return nil, fmt.Errorf("unsupported PBKDF2 PRF %v", kdf.PRF.Algorithm)
	}
	var keyLen int
	switch enc := params.EncryptionScheme.Algorithm; {
	case enc.Equal(oidAES128CBC):
		keyLen = 16
	case enc.Equal(oidAES192CBC):
		keyLen = 24
	case enc.Equal(oidAES256CBC):
		keyLen = 32
	default:
		return nil, fmt.Errorf("unsupported key cipher %v (only AES-CBC is supported)", enc)
	}
	var iv []byte
	if _, err := asn1.Unmarshal(params.EncryptionScheme.Parameters.FullBytes, &iv); err != nil {
		return nil, fmt.Errorf("cipher IV: %w", err)
	}
	key, err := pbkdf2.Key(h, string(passphrase), kdf.Salt, kdf.IterationCount, keyLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	data := info.EncryptedData
	if len(iv) != block.BlockSize() || len(data) == 0 || len(data)%block.BlockSize() != 0 {
		return nil, errors.New("malformed encrypted key")
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	pad := int(out[len(out)-1])
	if pad == 0 || pad > block.BlockSize() || pad > len(out) {
		return nil, errors.New("wrong passphrase or corrupted key")
	}
	for _, b := range out[len(out)-pad:] {
		if int(b) != pad {
			return nil, errors.New("wrong passphrase or corrupted key")
		}
	}
	return out[:len(out)-pad], nil
}

// CheckCAPath loads the certificates of a CA file or directory (admin
// check) and returns how many it holds.
func CheckCAPath(path string) (int, error) {
	n, err := loadCAs(x509.NewCertPool(), path)
	if err == nil && n == 0 {
		err = fmt.Errorf("no CA certificate found in %s", path)
	}
	return n, err
}
