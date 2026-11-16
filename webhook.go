package kha

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// WebhookVerificationError reports a failed Trigger notification signature
// check (VerifyWebhookSignature). It is a local validation failure, not an
// HTTP error: like SessionError it sits next to APIError and carries no
// status code. Classify with errors.AsType[*WebhookVerificationError]; Reason
// is diagnostic text, not a stable vocabulary.
type WebhookVerificationError struct {
	// Reason describes why verification failed (e.g. the timestamp left the
	// tolerance window, or no listed signature matched).
	Reason string `json:"reason"`
}

// Error implements error.
func (e *WebhookVerificationError) Error() string {
	return "kha: webhook signature verification failed: " + e.Reason
}

// webhookTolerance is the replay-protection window applied to the
// Webhook-Timestamp header (standard-webhooks recommends 3–5 minutes).
const webhookTolerance = 5 * time.Minute

// VerifyWebhookSignature verifies one Trigger notification delivery against
// the standard-webhooks scheme the API documents for TriggerNotification:
// the signature content is "<Webhook-Id>.<Webhook-Timestamp>.<raw body>",
// HMAC-SHA256 keyed with the delivery secret (the raw value of the Vault's
// webhook_secret credential), base64-encoded, and the Webhook-Signature
// header carries a space-separated list of "<version>,<signature>" entries —
// any matching "v1" entry verifies the delivery, so secret rotation (several
// v1 entries) and future versions are tolerated.
//
// The timestamp must be Unix seconds within ±5 minutes of the current time
// (replay protection); a signature that verifies against an out-of-window
// timestamp is still rejected. A nil error means the delivery is authentic
// and fresh; every failure is a *WebhookVerificationError.
func VerifyWebhookSignature(secret string, id, timestamp, signatureHeader string, body []byte) error {
	return verifyWebhookSignature(secret, id, timestamp, signatureHeader, body, time.Now())
}

// verifyWebhookSignature is VerifyWebhookSignature with the clock injected
// for tests.
func verifyWebhookSignature(secret string, id, timestamp, signatureHeader string, body []byte, now time.Time) error {
	fail := func(format string, args ...any) error {
		return &WebhookVerificationError{Reason: fmt.Sprintf(format, args...)}
	}
	if secret == "" {
		return fail("empty webhook secret")
	}
	if id == "" {
		return fail("missing Webhook-Id header")
	}
	if timestamp == "" {
		return fail("missing Webhook-Timestamp header")
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return fail("Webhook-Timestamp %q is not Unix seconds", timestamp)
	}
	if delta := now.Sub(time.Unix(ts, 0)); delta > webhookTolerance || delta < -webhookTolerance {
		return fail("Webhook-Timestamp %s is outside the ±%s tolerance window", time.Unix(ts, 0).UTC(), webhookTolerance)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%s.%s.", id, timestamp) // hash writes cannot fail
	mac.Write(body)
	expected := mac.Sum(nil)

	v1Seen := false
	for entry := range strings.FieldsSeq(signatureHeader) {
		version, sig, found := strings.Cut(entry, ",")
		if !found || version != "v1" {
			continue // other versions and malformed entries are ignored
		}
		decoded, err := base64.StdEncoding.DecodeString(sig)
		if err != nil {
			continue // not a parseable v1 signature; keep looking
		}
		v1Seen = true
		if hmac.Equal(decoded, expected) {
			return nil
		}
	}
	if !v1Seen {
		return fail("no v1 signature in the Webhook-Signature header")
	}
	return fail("no listed v1 signature matches the delivery")
}
