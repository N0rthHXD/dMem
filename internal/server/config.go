package server

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
)

func (c Config) validate() error {
	if c.ListenAddress == "" {
		return errors.New("server: listen address is required")
	}
	if _, _, err := net.SplitHostPort(c.ListenAddress); err != nil {
		return fmt.Errorf("server: invalid listen address: %w", err)
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return errors.New("server: TLS certificate and key must be provided together")
	}
	return nil
}

func loadTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	if err := validateRegularFile(certFile); err != nil {
		return nil, err
	}
	if err := validateRegularFile(keyFile); err != nil {
		return nil, err
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("server: load TLS key pair: %w", err)
	}
	return &tls.Config{
		MinVersion:   DefaultTLSMinVersion,
		Certificates: []tls.Certificate{certificate},
	}, nil
}

func validateRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("server: TLS file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("server: TLS file must be a regular file: %s", path)
	}
	return nil
}
