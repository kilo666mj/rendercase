package securetoken

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func ID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return strings.ToLower(idEncoding.EncodeToString(buf)), nil
}

func Secret() (plain string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", nil, err
	}
	plain = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(plain))
	return plain, sum[:], nil
}

func Hash(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}

func Sign(key []byte, subject string, expiry time.Time) string {
	payload := subject + "." + strconv.FormatInt(expiry.Unix(), 10)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

func Verify(key []byte, token string, now time.Time) (subject string, expiry time.Time, err error) {
	encoded, encodedSig, ok := strings.Cut(token, ".")
	if !ok {
		return "", time.Time{}, errors.New("malformed signed token")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", time.Time{}, errors.New("malformed signed token")
	}
	payload := string(payloadBytes)
	dot := strings.LastIndexByte(payload, '.')
	if dot < 1 || dot == len(payload)-1 {
		return "", time.Time{}, errors.New("malformed signed token")
	}
	subject, expiryText := payload[:dot], payload[dot+1:]
	expiryUnix, err := strconv.ParseInt(expiryText, 10, 64)
	if err != nil {
		return "", time.Time{}, errors.New("malformed signed token")
	}
	provided, err := base64.RawURLEncoding.DecodeString(encodedSig)
	if err != nil {
		return "", time.Time{}, errors.New("malformed signed token")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return "", time.Time{}, errors.New("invalid signed token")
	}
	expiry = time.Unix(expiryUnix, 0)
	if !now.Before(expiry) {
		return "", time.Time{}, fmt.Errorf("signed token expired at %s", expiry.UTC().Format(time.RFC3339))
	}
	return subject, expiry, nil
}
