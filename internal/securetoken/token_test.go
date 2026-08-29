package securetoken

import (
	"bytes"
	"testing"
	"time"
)

func TestSecretAndHash(t *testing.T) {
	plain, hash, err := Secret()
	if err != nil {
		t.Fatal(err)
	}
	if plain == "" || !bytes.Equal(hash, Hash(plain)) {
		t.Fatal("secret hash mismatch")
	}
}

func TestSignedToken(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	now := time.Unix(1_800_000_000, 0)
	token := Sign(key, "artifact.1", now.Add(time.Minute))
	subject, _, err := Verify(key, token, now)
	if err != nil || subject != "artifact.1" {
		t.Fatalf("Verify = %q, %v", subject, err)
	}
	if _, _, err := Verify(key, token+"x", now); err == nil {
		t.Fatal("tampered token accepted")
	}
	if _, _, err := Verify(key, token, now.Add(time.Minute)); err == nil {
		t.Fatal("expired token accepted")
	}
}
