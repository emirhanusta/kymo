package authtest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// FakeIdP is an in-process OpenID Connect provider: discovery, JWKS and a
// token endpoint that verifies PKCE and signs RS256 ID tokens.
type FakeIdP struct {
	Server       *httptest.Server
	ClientID     string
	ClientSecret string
	Code         string

	mu        sync.Mutex
	key       *rsa.PrivateKey
	challenge string
	nonce     string
	state     string
	claims    map[string]any
	// Aud overrides the token audience (default ClientID); NonceOverride the nonce.
	Aud           string
	NonceOverride string
}

// NewFakeIdP starts the provider; it stops with the test.
func NewFakeIdP(t *testing.T) *FakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &FakeIdP{ClientID: "kymo-client", ClientSecret: "kymo-secret", Code: "fake-code", key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.Issuer(), "authorization_endpoint": f.Issuer() + "/authorize", "token_endpoint": f.Issuer() + "/token",
			"jwks_uri": f.Issuer() + "/jwks", "response_types_supported": []string{"code"},
			"subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}}
		_ = json.NewEncoder(w).Encode(set)
	})
	mux.HandleFunc("/token", f.token)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

// Issuer is the provider's issuer URL.
func (f *FakeIdP) Issuer() string { return f.Server.URL }

// Login simulates the browser leg: it reads the authorization URL produced by
// KYMO, remembers PKCE challenge / nonce, sets the claims the next ID token
// will carry (iss, aud, exp... are added) and returns the state to send back.
func (f *FakeIdP) Login(t *testing.T, authURL string, claims map[string]any) (state, code string) {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" ||
		q.Get("client_id") != f.ClientID || q.Get("response_type") != "code" {
		t.Fatalf("authorization request lacks PKCE/state/nonce: %s", authURL)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.challenge, f.nonce, f.state, f.claims = q.Get("code_challenge"), q.Get("nonce"), q.Get("state"), claims
	return q.Get("state"), f.Code
}

func (f *FakeIdP) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	user, pass, hasBasic := r.BasicAuth()
	if hasBasic {
		user, _ = url.QueryUnescape(user)
		pass, _ = url.QueryUnescape(pass)
	} else {
		user, pass = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	fail := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": msg})
	}
	if user != f.ClientID || pass != f.ClientSecret {
		fail("bad client credentials")
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("code") != f.Code {
		fail("bad code")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
		fail("PKCE verification failed")
		return
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	aud := f.Aud
	if aud == "" {
		aud = f.ClientID
	}
	nonce := f.nonce
	if f.NonceOverride != "" {
		nonce = f.NonceOverride
	}
	now := time.Now()
	std := jwt.Claims{Issuer: f.Issuer(), Audience: jwt.Audience{aud}, IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(5 * time.Minute))}
	extra := map[string]any{"nonce": nonce}
	for k, v := range f.claims {
		extra[k] = v
	}
	raw, err := jwt.Signed(signer).Claims(std).Claims(extra).Serialize()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 300, "id_token": raw})
}
