package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"

	"cato/internal/config"
	"cato/internal/devtls"
)

// devCertCommand implements `cato dev-cert --out DIR`: it writes (or reuses)
// a self-signed development certificate and prints only the SPKI SHA-256
// pin on stdout, for Chromium's --ignore-certificate-errors-spki-list.
func devCertCommand(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("dev-cert", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "directory for cert.pem and key.pem (created 0700)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("usage: cato dev-cert --out DIR")
	}
	certFile, keyFile, err := devtls.Ensure(*out, devtls.DefaultNames())
	if err != nil {
		return err
	}
	spki, err := devtls.SPKIFingerprint(certFile)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "certificate: %s\nkey:         %s\n", certFile, keyFile)
	fmt.Fprintln(stdout, spki)
	return nil
}

// prepareTLS validates the transport configuration and, for CATO_DEV_TLS,
// generates (or reuses) the development certificate.
func prepareTLS(cfg *config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if !cfg.DevTLS {
		return nil
	}
	certFile, keyFile, err := devtls.Ensure(cfg.DevTLSDir, devtls.DefaultNames())
	if err != nil {
		return err
	}
	cfg.TLSCertFile, cfg.TLSKeyFile = certFile, keyFile
	if spki, err := devtls.SPKIFingerprint(certFile); err == nil {
		log.Printf("dev TLS: self-signed certificate %s (SPKI sha256 %s)", certFile, spki)
	}
	return nil
}
