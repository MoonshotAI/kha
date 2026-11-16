package kha

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	webhookNow  = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	webhookBody = []byte(`{"type":"event","id":"dly_123","data":{"type":"trigger_run.succeeded","id":"run_123"}}`)
)

// webhookSign computes a valid v1 signature entry, mirroring the delivery
// side of the standard-webhooks scheme.
func webhookSign(secret, id, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%s.%s.", id, ts)
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// webhookVerify runs the internal verify with the test clock and a fresh
// self-computed signature unless overridden.
func webhookVerify(secret, id, ts, sigHeader string, body []byte) error {
	return verifyWebhookSignature(secret, id, ts, sigHeader, body, webhookNow)
}

func TestVerifyWebhookSignature(t *testing.T) {
	id := "msg_01h455vb4pex5vsknk084sn02q"
	ts := strconv.FormatInt(webhookNow.Unix(), 10)
	secret := "delivery-secret"
	sig := webhookSign(secret, id, ts, webhookBody)

	t.Run("valid signature verifies", func(t *testing.T) {
		if err := webhookVerify(secret, id, ts, sig, webhookBody); err != nil {
			t.Fatalf("verify: %v", err)
		}
	})

	t.Run("tampered body rejected", func(t *testing.T) {
		err := webhookVerify(secret, id, ts, sig, []byte(`{"type":"event","id":"dly_123","data":{"type":"trigger_run.failed","id":"run_123"}}`))
		assertWebhookError(t, err, "no listed v1 signature matches")
	})

	t.Run("empty secret rejects a matching signature", func(t *testing.T) {
		err := webhookVerify("", id, ts, webhookSign("", id, ts, webhookBody), webhookBody)
		assertWebhookError(t, err, "empty webhook secret")
	})

	t.Run("wrong secret rejected", func(t *testing.T) {
		err := webhookVerify("other-secret", id, ts, sig, webhookBody)
		assertWebhookError(t, err, "no listed v1 signature matches")
	})

	t.Run("timestamp too old rejected", func(t *testing.T) {
		old := strconv.FormatInt(webhookNow.Add(-webhookTolerance-time.Second).Unix(), 10)
		// The signature is valid for its timestamp; freshness must still fail.
		err := webhookVerify(secret, id, old, webhookSign(secret, id, old, webhookBody), webhookBody)
		assertWebhookError(t, err, "outside the ±5m0s tolerance window")
	})

	t.Run("timestamp too far in the future rejected", func(t *testing.T) {
		future := strconv.FormatInt(webhookNow.Add(webhookTolerance+time.Second).Unix(), 10)
		err := webhookVerify(secret, id, future, webhookSign(secret, id, future, webhookBody), webhookBody)
		assertWebhookError(t, err, "tolerance window")
	})

	t.Run("timestamp at the window edge verifies", func(t *testing.T) {
		edge := strconv.FormatInt(webhookNow.Add(-webhookTolerance).Unix(), 10)
		if err := webhookVerify(secret, id, edge, webhookSign(secret, id, edge, webhookBody), webhookBody); err != nil {
			t.Fatalf("verify at the edge: %v", err)
		}
	})

	t.Run("multi-version header tolerated", func(t *testing.T) {
		header := "v2,bm90LWEtdjEtc2ln v1bogus " + sig
		if err := webhookVerify(secret, id, ts, header, webhookBody); err != nil {
			t.Fatalf("verify: %v", err)
		}
	})

	t.Run("any matching v1 entry verifies (rotation)", func(t *testing.T) {
		stale := webhookSign("old-secret", id, ts, webhookBody)
		header := stale + "  " + sig // extra whitespace between entries
		if err := webhookVerify(secret, id, ts, header, webhookBody); err != nil {
			t.Fatalf("verify: %v", err)
		}
	})

	t.Run("malformed entries ignored", func(t *testing.T) {
		header := "garbage v1,!!!not-base64!!! , v1, " + sig
		if err := webhookVerify(secret, id, ts, header, webhookBody); err != nil {
			t.Fatalf("verify: %v", err)
		}
	})

	t.Run("no v1 entry fails", func(t *testing.T) {
		err := webhookVerify(secret, id, ts, "v2,bm90LWEtdjEtc2ln garbage", webhookBody)
		assertWebhookError(t, err, "no v1 signature")
	})

	t.Run("empty signature header fails", func(t *testing.T) {
		err := webhookVerify(secret, id, ts, "", webhookBody)
		assertWebhookError(t, err, "no v1 signature")
	})

	t.Run("missing id fails", func(t *testing.T) {
		err := webhookVerify(secret, "", ts, sig, webhookBody)
		assertWebhookError(t, err, "missing Webhook-Id")
	})

	t.Run("missing timestamp fails", func(t *testing.T) {
		err := webhookVerify(secret, id, "", sig, webhookBody)
		assertWebhookError(t, err, "missing Webhook-Timestamp")
	})

	t.Run("non-integer timestamp fails", func(t *testing.T) {
		err := webhookVerify(secret, id, "2026-09-01T12:00:00Z", sig, webhookBody)
		assertWebhookError(t, err, "not Unix seconds")
	})
}

// assertWebhookError requires a *WebhookVerificationError whose Reason
// carries the expected fragment.
func assertWebhookError(t *testing.T, err error, wantReason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("verify succeeded, want failure containing %q", wantReason)
	}
	vErr, ok := errors.AsType[*WebhookVerificationError](err)
	if !ok {
		t.Fatalf("error %T is not a *WebhookVerificationError: %v", err, err)
	}
	if !strings.Contains(vErr.Reason, wantReason) {
		t.Fatalf("Reason = %q, want it to contain %q", vErr.Reason, wantReason)
	}
}
