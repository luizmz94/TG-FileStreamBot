package streamauth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const defaultFirebaseCertsURL = "https://www.googleapis.com/robot/v1/metadata/x509/securetoken@system.gserviceaccount.com"

type FirebaseClaims struct {
	Subject       string
	Email         string
	EmailVerified bool
}

type firebaseJWTHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

type firebaseVerifier struct {
	projectID string
	issuer    string
	certsURL  string
	client    *http.Client
	log       *zap.Logger

	mu          sync.RWMutex
	publicKeys  map[string]*rsa.PublicKey
	cacheExpiry time.Time
}

func newFirebaseVerifier(log *zap.Logger, projectID string, certsURL string) (*firebaseVerifier, error) {
	if projectID == "" {
		return nil, fmt.Errorf("firebase project id is required")
	}
	if certsURL == "" {
		certsURL = defaultFirebaseCertsURL
	}

	return &firebaseVerifier{
		projectID:  projectID,
		issuer:     "https://securetoken.google.com/" + projectID,
		certsURL:   certsURL,
		client:     &http.Client{Timeout: 5 * time.Second},
		log:        log.Named("FirebaseVerifier"),
		publicKeys: make(map[string]*rsa.PublicKey),
	}, nil
}

func (v *firebaseVerifier) VerifyToken(ctx context.Context, rawToken string) (*FirebaseClaims, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid jwt format")
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid jwt header: %w", err)
	}
	var header firebaseJWTHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("invalid jwt header json: %w", err)
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unexpected signing algorithm: %s", header.Alg)
	}
	if header.Kid == "" {
		return nil, fmt.Errorf("missing key id (kid)")
	}

	publicKey, err := v.getPublicKey(ctx, header.Kid)
	if err != nil {
		return nil, err
	}

	signingInput := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("invalid jwt signature encoding: %w", err)
	}
	hash := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, hash[:], signature); err != nil {
		return nil, fmt.Errorf("invalid jwt signature")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid jwt payload: %w", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, fmt.Errorf("invalid jwt payload json: %w", err)
	}

	now := time.Now().Unix()
	exp, err := int64Claim(payload, "exp")
	if err != nil {
		return nil, err
	}
	iat, err := int64Claim(payload, "iat")
	if err != nil {
		return nil, err
	}

	if now > exp {
		return nil, fmt.Errorf("token expired")
	}
	// Allow small clock skew (up to 5 minutes).
	if iat > now+300 {
		return nil, fmt.Errorf("token issued in the future")
	}

	aud, err := stringClaim(payload, "aud")
	if err != nil {
		return nil, err
	}
	if aud != v.projectID {
		return nil, fmt.Errorf("invalid audience")
	}

	iss, err := stringClaim(payload, "iss")
	if err != nil {
		return nil, err
	}
	if iss != v.issuer {
		return nil, fmt.Errorf("invalid issuer")
	}

	sub, err := stringClaim(payload, "sub")
	if err != nil {
		return nil, err
	}
	if len(sub) == 0 || len(sub) > 128 {
		return nil, fmt.Errorf("invalid subject")
	}

	email, _ := optionalStringClaim(payload, "email")
	emailVerified, _ := optionalBoolClaim(payload, "email_verified")

	return &FirebaseClaims{
		Subject:       sub,
		Email:         email,
		EmailVerified: emailVerified,
	}, nil
}

func (v *firebaseVerifier) getPublicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	now := time.Now()

	v.mu.RLock()
	key, exists := v.publicKeys[kid]
	cacheValid := now.Before(v.cacheExpiry) && len(v.publicKeys) > 0
	v.mu.RUnlock()

	if exists && cacheValid {
		return key, nil
	}

	forceRefresh := !exists
	if err := v.refreshKeys(ctx, forceRefresh); err != nil {
		return nil, fmt.Errorf("failed to refresh firebase certs: %w", err)
	}

	v.mu.RLock()
	defer v.mu.RUnlock()

	key, exists = v.publicKeys[kid]
	if !exists {
		return nil, fmt.Errorf("firebase signing key not found for kid=%s", kid)
	}
	return key, nil
}

func (v *firebaseVerifier) refreshKeys(ctx context.Context, force bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if !force && time.Now().Before(v.cacheExpiry) && len(v.publicKeys) > 0 {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.certsURL, nil)
	if err != nil {
		return err
	}

	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected cert endpoint status: %s", resp.Status)
	}

	var certMap map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&certMap); err != nil {
		return err
	}
	if len(certMap) == 0 {
		return fmt.Errorf("empty firebase cert response")
	}

	parsedKeys := make(map[string]*rsa.PublicKey, len(certMap))
	for kid, certPEM := range certMap {
		publicKey, err := parseRSAPublicKeyFromPEM(certPEM)
		if err != nil {
			return fmt.Errorf("parse cert %s: %w", kid, err)
		}
		parsedKeys[kid] = publicKey
	}

	ttl := parseCacheMaxAge(resp.Header.Get("Cache-Control"))
	if ttl <= 0 {
		ttl = time.Hour
	}

	v.publicKeys = parsedKeys
	v.cacheExpiry = time.Now().Add(ttl)
	v.log.Debug("Firebase cert cache refreshed",
		zap.Int("keyCount", len(parsedKeys)),
		zap.Duration("ttl", ttl))
	return nil
}

func parseRSAPublicKeyFromPEM(certPEM string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, fmt.Errorf("invalid pem block")
	}

	if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
		rsaKey, ok := cert.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("certificate does not contain rsa key")
		}
		return rsaKey, nil
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not rsa")
	}
	return rsaKey, nil
}

func parseCacheMaxAge(cacheControl string) time.Duration {
	if cacheControl == "" {
		return 0
	}

	directives := strings.Split(cacheControl, ",")
	for _, directive := range directives {
		trimmed := strings.TrimSpace(directive)
		if !strings.HasPrefix(trimmed, "max-age=") {
			continue
		}
		seconds, err := strconv.Atoi(strings.TrimPrefix(trimmed, "max-age="))
		if err != nil || seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}

	return 0
}

func stringClaim(payload map[string]any, key string) (string, error) {
	val, ok := payload[key]
	if !ok {
		return "", fmt.Errorf("missing claim: %s", key)
	}
	strVal, ok := val.(string)
	if !ok || strVal == "" {
		return "", fmt.Errorf("invalid claim: %s", key)
	}
	return strVal, nil
}

func optionalStringClaim(payload map[string]any, key string) (string, bool) {
	val, ok := payload[key]
	if !ok {
		return "", false
	}
	strVal, ok := val.(string)
	if !ok {
		return "", false
	}
	return strVal, true
}

func optionalBoolClaim(payload map[string]any, key string) (bool, bool) {
	val, ok := payload[key]
	if !ok {
		return false, false
	}
	boolVal, ok := val.(bool)
	if !ok {
		return false, false
	}
	return boolVal, true
}

func int64Claim(payload map[string]any, key string) (int64, error) {
	val, ok := payload[key]
	if !ok {
		return 0, fmt.Errorf("missing claim: %s", key)
	}

	switch n := val.(type) {
	case float64:
		return int64(n), nil
	case int64:
		return n, nil
	case json.Number:
		return n.Int64()
	case string:
		parsed, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid claim %s: %w", key, err)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("invalid claim type for %s", key)
	}
}
