package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type TokenValidator interface {
	ValidateToken(ctx context.Context, tokenString string) (*Claims, error)
}

type Claims struct {
	jwt.RegisteredClaims
	ClientID    string   `json:"azp"`
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

func (c *Claims) HasRole(role string) bool {
	for _, r := range c.RealmAccess.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// JWK representa uma chave pública no padrão RFC 7517.
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type JWKS struct {
	Keys []JWK `json:"keys"`
}

type KeycloakValidator struct {
	jwksURL    string
	issuer     string
	httpClient *http.Client
	mu         sync.RWMutex
	keys       map[string]*rsa.PublicKey
	lastFetch  time.Time
	cacheTTL   time.Duration
}

func NewKeycloakValidator(jwksURL, issuer string) *KeycloakValidator {
	return &KeycloakValidator{
		jwksURL: jwksURL,
		issuer:  issuer,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		keys:     make(map[string]*rsa.PublicKey),
		cacheTTL: 15 * time.Minute,
	}
}

func (v *KeycloakValidator) ValidateToken(ctx context.Context, tokenString string) (*Claims, error) {
	claims := &Claims{}

	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}

		kid, ok := t.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, errors.New("missing kid header in jwt")
		}

		pubKey, err := v.getKey(ctx, kid)
		if err != nil {
			return nil, err
		}
		return pubKey, nil
	})

	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	if !token.Valid {
		return nil, errors.New("token is not valid")
	}

	// Validação de claims essenciais
	if v.issuer != "" && !strings.EqualFold(claims.Issuer, v.issuer) {
		return nil, fmt.Errorf("issuer mismatch: expected %q, got %q", v.issuer, claims.Issuer)
	}

	if claims.ExpiresAt != nil && claims.ExpiresAt.Before(time.Now().UTC()) {
		return nil, errors.New("token expired")
	}

	return claims, nil
}

func (v *KeycloakValidator) getKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	key, ok := v.keys[kid]
	fresh := time.Since(v.lastFetch) < v.cacheTTL
	v.mu.RUnlock()

	if ok && fresh {
		return key, nil
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	// Double check
	if key, ok := v.keys[kid]; ok && time.Since(v.lastFetch) < v.cacheTTL {
		return key, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create jwks request: %w", err)
	}

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks responded with status %d", resp.StatusCode)
	}

	var jwks JWKS
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, fmt.Errorf("decode jwks response: %w", err)
	}

	v.keys = make(map[string]*rsa.PublicKey)
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pubKey, err := parseRSAPublicKey(k.N, k.E)
		if err != nil {
			continue
		}
		v.keys[k.Kid] = pubKey
	}
	v.lastFetch = time.Now()

	found, ok := v.keys[kid]
	if !ok {
		return nil, fmt.Errorf("public key not found for kid %q", kid)
	}
	return found, nil
}

func parseRSAPublicKey(nStr, eStr string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
	if err != nil {
		return nil, fmt.Errorf("decode modulus: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
	if err != nil {
		return nil, fmt.Errorf("decode exponent: %w", err)
	}

	var eVal int
	for _, b := range eBytes {
		eVal = (eVal << 8) | int(b)
	}

	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: eVal,
	}, nil
}
