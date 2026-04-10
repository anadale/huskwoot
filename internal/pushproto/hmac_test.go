package pushproto_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/pushproto"
)

func TestCanonical_StableOrderingOfFields(t *testing.T) {
	c1 := pushproto.Canonical("POST", "/v1/push", "1713513600", "aabbcc")
	c2 := pushproto.Canonical("POST", "/v1/push", "1713513600", "ddeeff")

	if string(c1) == string(c2) {
		t.Error("different bodyHash values should produce different canonical strings")
	}

	c1b := pushproto.Canonical("POST", "/v1/push", "1713513600", "aabbcc")
	if string(c1) != string(c1b) {
		t.Error("canonical should be deterministic")
	}

	// Verify that field order is fixed: METHOD\nPATH\nTS\nbodyHash.
	want := "POST\n/v1/push\n1713513600\naabbcc"
	if string(c1) != want {
		t.Errorf("canonical = %q, want %q", string(c1), want)
	}
}

func TestSign_ReproducibleForSameInputs(t *testing.T) {
	secret := []byte("supersecret")
	body := []byte(`{"deviceId":"dev1"}`)
	ts := "1713513600"

	sig1 := pushproto.Sign(secret, "POST", "/v1/push", ts, body)
	sig2 := pushproto.Sign(secret, "POST", "/v1/push", ts, body)

	if sig1 != sig2 {
		t.Error("Sign should be deterministic for the same inputs")
	}
	if sig1 == "" {
		t.Error("Sign should not return an empty string")
	}
}

func TestVerify_RejectsTampered(t *testing.T) {
	secret := []byte("supersecret")
	body := []byte(`{"deviceId":"dev1"}`)
	ts := "1713513600"

	sig := pushproto.Sign(secret, "POST", "/v1/push", ts, body)

	cases := []struct {
		name      string
		method    string
		path      string
		timestamp string
		body      []byte
	}{
		{"изменён метод", "GET", "/v1/push", ts, body},
		{"изменён путь", "POST", "/v1/other", ts, body},
		{"изменён timestamp", "POST", "/v1/push", "9999999999", body},
		{"изменено тело", "POST", "/v1/push", ts, []byte(`{"deviceId":"evil"}`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := pushproto.Verify(secret, sig, tc.method, tc.path, tc.timestamp, tc.body)
			if err == nil {
				t.Error("Verify should return an error when data is tampered")
			}
		})
	}
}

func TestVerifyTimestamp_RejectsSkewOverLimit(t *testing.T) {
	now := time.Unix(1713513600, 0)
	skew := 5 * time.Minute

	tooOld := fmt.Sprintf("%d", now.Add(-6*time.Minute).Unix())
	tooNew := fmt.Sprintf("%d", now.Add(6*time.Minute).Unix())
	exact := fmt.Sprintf("%d", now.Unix())
	withinSkew := fmt.Sprintf("%d", now.Add(-4*time.Minute).Unix())

	if err := pushproto.VerifyTimestamp(tooOld, now, skew); err == nil {
		t.Error("ts=now-6m should be rejected")
	}
	if err := pushproto.VerifyTimestamp(tooNew, now, skew); err == nil {
		t.Error("ts=now+6m should be rejected")
	}
	if err := pushproto.VerifyTimestamp(exact, now, skew); err != nil {
		t.Errorf("ts=now should be accepted: %v", err)
	}
	if err := pushproto.VerifyTimestamp(withinSkew, now, skew); err != nil {
		t.Errorf("ts=now-4m should be accepted: %v", err)
	}
}

func TestVerify_ValidatesBodySHA(t *testing.T) {
	secret := []byte("mysecret")
	ts := "1713513600"

	// Empty body is valid.
	emptySig := pushproto.Sign(secret, "PUT", "/v1/registrations/dev", ts, []byte{})
	if err := pushproto.Verify(secret, emptySig, "PUT", "/v1/registrations/dev", ts, []byte{}); err != nil {
		t.Errorf("empty body should be accepted: %v", err)
	}

	// nil body is equivalent to empty.
	nilSig := pushproto.Sign(secret, "PUT", "/v1/registrations/dev", ts, nil)
	if emptySig != nilSig {
		t.Error("nil and empty body should produce the same signature")
	}

	// Single-byte body differs from empty.
	oneByteSig := pushproto.Sign(secret, "PUT", "/v1/registrations/dev", ts, []byte("x"))
	if emptySig == oneByteSig {
		t.Error("different bodies should produce different signatures")
	}
}
