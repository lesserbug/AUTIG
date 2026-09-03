package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	replicas := flag.Int("replicas", 0, "number of replica key pairs")
	output := flag.String("out", "keys", "output directory")
	flag.Parse()
	if *replicas <= 0 {
		fmt.Fprintln(os.Stderr, "replicas must be positive")
		os.Exit(2)
	}
	if err := os.MkdirAll(*output, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for replicaID := 0; replicaID < *replicas; replicaID++ {
		prefix := filepath.Join(*output, fmt.Sprintf("replica_%d", replicaID))
		if err := writeKeyPair(prefix); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if err := writeKeyPair(filepath.Join(*output, "leader")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeKeyPair(prefix string) error {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return err
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	if err := os.WriteFile(prefix+"_private.pem", privatePEM, 0o600); err != nil {
		return err
	}
	return os.WriteFile(prefix+"_public.pem", publicPEM, 0o644)
}
