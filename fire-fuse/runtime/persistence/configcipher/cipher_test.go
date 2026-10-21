package configcipher

import (
	"errors"
	"testing"
)

func TestCipherBindsConfigurationIdentity(t *testing.T) {
	cipher, err := New(make([]byte, KeyBytes))
	if err != nil {
		t.Fatal(err)
	}
	nonce, ciphertext, err := cipher.Seal("app", "app-1", "TOKEN", "secret")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := cipher.Open("app", "app-1", "TOKEN", nonce, ciphertext)
	if err != nil || plaintext != "secret" {
		t.Fatalf("Open() = %q, %v", plaintext, err)
	}
	if _, err := cipher.Open("app", "app-2", "TOKEN", nonce, ciphertext); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("Open() = %v, want ErrAuthentication", err)
	}
}

func TestCipherRequiresExactKeySize(t *testing.T) {
	if _, err := New(make([]byte, KeyBytes-1)); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("New() = %v, want ErrInvalidKey", err)
	}
}
