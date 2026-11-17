package secret

import (
	"bytes"
	"errors"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	b, _ := New("correct horse")
	sealed, err := b.Seal([]byte("apiVersion: v1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("apiVersion")) {
		t.Fatal("plaintext leaked into ciphertext")
	}
	got, err := b.Open(sealed)
	if err != nil || string(got) != "apiVersion: v1" {
		t.Fatalf("round trip failed: %q %v", got, err)
	}

	other, _ := New("wrong key")
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("decrypting with the wrong key must fail")
	}
}

func TestUnconfigured(t *testing.T) {
	b, _ := New("")
	if b.Configured() {
		t.Fatal("empty passphrase must be unconfigured")
	}
	if _, err := b.Seal([]byte("x")); !errors.Is(err, ErrNoKey) {
		t.Fatalf("want ErrNoKey, got %v", err)
	}
}
