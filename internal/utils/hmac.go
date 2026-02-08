package utils

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

// ValidateHMACSignature validates the HMAC signature and expiration for direct stream URLs
// Returns nil if valid, error otherwise
func ValidateHMACSignature(secret string, messageID int, signature string, expiration string) error {
	if secret == "" {
		// If no secret is configured, allow access (backward compatibility)
		return nil
	}

	if signature == "" || expiration == "" {
		return fmt.Errorf("missing signature or expiration")
	}

	// Parse expiration timestamp
	exp, err := strconv.ParseInt(expiration, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid expiration timestamp")
	}

	// Check if expired
	now := time.Now().Unix()
	if now > exp {
		return fmt.Errorf("signature expired")
	}

	// Compute expected signature
	data := fmt.Sprintf("%d:%d", messageID, exp)
	expectedSig := ComputeHMAC(secret, data)

	// Compare signatures (constant time comparison)
	if !hmac.Equal([]byte(signature), []byte(expectedSig)) {
		return fmt.Errorf("invalid signature")
	}

	return nil
}

// ComputeHMAC computes HMAC-SHA256 signature for the given data
func ComputeHMAC(secret string, data string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// GenerateSignedURL generates a signed URL for a message ID
// This is a helper function for generating URLs on the backend
func GenerateSignedURL(secret string, messageID int, expiresIn int64) (signature string, expiration int64) {
	exp := time.Now().Unix() + expiresIn
	data := fmt.Sprintf("%d:%d", messageID, exp)
	sig := ComputeHMAC(secret, data)
	return sig, exp
}
