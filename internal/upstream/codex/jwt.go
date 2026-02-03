package codex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// JWTClaims represents the claims section of a Codex JWT token
type JWTClaims struct {
	Email    string        `json:"email"`
	Exp      int64         `json:"exp"`
	Iat      int64         `json:"iat"`
	AuthInfo CodexAuthInfo `json:"https://api.openai.com/auth"`
}

// CodexAuthInfo contains ChatGPT account details from JWT claims
type CodexAuthInfo struct {
	ChatgptAccountID string `json:"chatgpt_account_id"`
	ChatgptPlanType  string `json:"chatgpt_plan_type"` // plus, pro, team
	ChatgptUserID    string `json:"chatgpt_user_id"`
}

// ParseJWT parses a JWT token string and extracts its claims
// Note: This does NOT verify the signature, only extracts the payload
func ParseJWT(token string) (*JWTClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format: expected 3 parts, got %d", len(parts))
	}

	// Decode the payload (second part)
	payload := parts[1]
	// Add padding if needed
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	data, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims JWTClaims
	if err := json.Unmarshal(data, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse JWT claims: %w", err)
	}
	return &claims, nil
}
