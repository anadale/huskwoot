package pushproto

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Canonical returns the canonical string used to compute the HMAC.
// Format: METHOD\nPATH\nTIMESTAMP\nlower(hex(sha256(body)))
// bodyHashHex must already be computed by the caller (lowercase hex).
func Canonical(method, path, timestamp, bodyHashHex string) []byte {
	var b strings.Builder
	b.WriteString(method)
	b.WriteByte('\n')
	b.WriteString(path)
	b.WriteByte('\n')
	b.WriteString(timestamp)
	b.WriteByte('\n')
	b.WriteString(bodyHashHex)
	return []byte(b.String())
}

// bodySHA256Hex computes lower(hex(SHA256(body))).
func bodySHA256Hex(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// Sign returns hex(HMAC-SHA256(secret, canonical(method, path, ts, sha256(body)))).
func Sign(secret []byte, method, path, timestamp string, body []byte) string {
	bodyHash := bodySHA256Hex(body)
	msg := Canonical(method, path, timestamp, bodyHash)
	mac := hmac.New(sha256.New, secret)
	mac.Write(msg)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify checks the signature. Returns an error if the signature is invalid.
func Verify(secret []byte, sigHex, method, path, timestamp string, body []byte) error {
	expected := Sign(secret, method, path, timestamp, body)
	expectedBytes, _ := hex.DecodeString(expected)
	gotBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return errors.New("invalid signature format")
	}
	if subtle.ConstantTimeCompare(expectedBytes, gotBytes) != 1 {
		return errors.New("invalid signature")
	}
	return nil
}

// VerifyTimestamp checks that ts (unix seconds) is within ±skew of now.
func VerifyTimestamp(tsHeader string, now time.Time, skew time.Duration) error {
	tsUnix, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid timestamp format: %w", err)
	}
	ts := time.Unix(tsUnix, 0)
	diff := now.Sub(ts)
	if diff < 0 {
		diff = -diff
	}
	if diff > skew {
		return fmt.Errorf("timestamp outside allowed window ±%v", skew)
	}
	return nil
}
