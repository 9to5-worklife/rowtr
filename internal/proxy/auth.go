package proxy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AuthHeader is the header a client must present on /v1/messages when the
// proxy requires authentication. The proxy strips it before forwarding, so the
// token never leaves the machine.
const AuthHeader = "x-rowtr-auth"

// LoadOrCreateToken returns the shared secret from path, generating a random
// 32-byte token on first use. The file is user-private (0o600): a process
// running as another user can neither read it to authenticate as a client nor
// answer the launcher's health challenge as a server.
func LoadOrCreateToken(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		if tok := strings.TrimSpace(string(data)); tok != "" {
			return tok, nil
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	tok := hex.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// HealthProof answers a client nonce: HMAC-SHA256 keyed by the shared token.
// A listener that can produce it holds the token file, which only the owning
// user can read — so `rowtr claude` can tell a real Rowtr proxy from a
// squatter that merely mimics the health JSON.
func HealthProof(token, nonce string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

func tokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
