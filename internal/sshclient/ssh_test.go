package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestFingerprintForType(t *testing.T) {
	_, edPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edSigner, err := ssh.NewSignerFromKey(edPrivate)
	if err != nil {
		t.Fatal(err)
	}
	rsaPrivate, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaSigner, err := ssh.NewSignerFromKey(rsaPrivate)
	if err != nil {
		t.Fatal(err)
	}

	keys := []knownhosts.KnownKey{{Key: edSigner.PublicKey()}}
	if _, ok := fingerprintForType(keys, rsaSigner.PublicKey().Type()); ok {
		t.Fatal("different key type was treated as a changed key")
	}
	want := ssh.FingerprintSHA256(edSigner.PublicKey())
	got, ok := fingerprintForType(keys, edSigner.PublicKey().Type())
	if !ok || got != want {
		t.Fatalf("fingerprintForType() = %q, %v; want %q, true", got, ok, want)
	}
}
