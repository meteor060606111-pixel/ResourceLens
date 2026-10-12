package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Authority struct {
	mu         sync.Mutex
	cert       *x509.Certificate
	key        *ecdsa.PrivateKey
	cache      map[string]*tls.Certificate
	certPath   string
	Thumbprint string
}

func serial() *big.Int { n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128)); return n }
func LoadAuthority(dir string) (*Authority, error) {
	a := &Authority{cache: map[string]*tls.Certificate{}, certPath: filepath.Join(dir, "ResourceLens-CA.crt")}
	keyPath := filepath.Join(dir, "ResourceLens-CA.key")
	certPEM, cErr := os.ReadFile(a.certPath)
	keyPEM, kErr := os.ReadFile(keyPath)
	if cErr == nil && kErr == nil {
		cb, _ := pem.Decode(certPEM)
		kb, _ := pem.Decode(keyPEM)
		if cb == nil || kb == nil {
			return nil, errors.New("证书文件损坏")
		}
		cert, err := x509.ParseCertificate(cb.Bytes)
		if err != nil {
			return nil, err
		}
		key, err := x509.ParseECPrivateKey(kb.Bytes)
		if err != nil {
			return nil, err
		}
		if !cert.IsCA || !key.PublicKey.Equal(cert.PublicKey) || time.Now().After(cert.NotAfter) {
			return nil, errors.New("证书已过期或证书与私钥不匹配")
		}
		a.cert = cert
		a.key = key
	} else if os.IsNotExist(cErr) && os.IsNotExist(kErr) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		template := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "ResourceLens Local Capture CA", Organization: []string{"ResourceLens"}}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(3, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			return nil, err
		}
		keyDER, _ := x509.MarshalECPrivateKey(key)
		if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
			return nil, err
		}
		if err = os.WriteFile(a.certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			return nil, err
		}
		a.cert, _ = x509.ParseCertificate(der)
		a.key = key
	} else {
		return nil, errors.New("证书或私钥缺失，请检查 data 目录，不要单独删除其中一个文件")
	}
	sum := sha1.Sum(a.cert.Raw)
	a.Thumbprint = hex.EncodeToString(sum[:])
	return a, nil
}
func (a *Authority) ForHost(host string) (*tls.Certificate, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c := a.cache[host]; c != nil {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	t := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(0, 1, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(host); ip != nil {
		t.IPAddresses = []net.IP{ip}
	} else {
		t.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, t, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return nil, err
	}
	cert := &tls.Certificate{Certificate: [][]byte{der, a.cert.Raw}, PrivateKey: key}
	if len(a.cache) > 1000 {
		a.cache = map[string]*tls.Certificate{}
	}
	a.cache[host] = cert
	return cert, nil
}
