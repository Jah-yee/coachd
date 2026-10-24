package oauth

import (
	"crypto/hmac"
	"crypto/sha256"
)

// Labels separating the HMAC keys derived from the one nonce key, so a blob
// signed for one purpose never verifies for another.
const (
	labelRequest = "coachd oauth request v1"
	labelDCR     = "coachd oauth dcr v1"
)

// deriveKey returns HMAC-SHA256(nonceKey, label).
func deriveKey(nonceKey []byte, label string) []byte {
	m := hmac.New(sha256.New, nonceKey)
	m.Write([]byte(label))
	return m.Sum(nil)
}

// blobMAC is HMAC-SHA256(key, payload): the MAC of the signed blobs, a DCR
// client_id and an authorization request token, each under its own key.
func blobMAC(key, payload []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(payload)
	return m.Sum(nil)
}
