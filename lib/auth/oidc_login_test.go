/*
 * Teleport
 * Copyright (C) 2026  Gravitational, Inc.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

// Characterization test net for the OIDC login service (lib/auth/oidc_login.go).

package auth_test

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/auth"
	"github.com/gravitational/teleport/lib/auth/authtest"
	"github.com/gravitational/teleport/lib/cryptosuites"
	"github.com/gravitational/teleport/lib/modules/modulestest"
	"github.com/gravitational/teleport/lib/services"
)

// fakeIDPServer is a configurable fake OIDC identity provider server.
// Tests call SetNonce to inject the nonce that the token endpoint embeds in
// the signed ID token, simulating a real IdP that stores nonces session-side.
// SetSigningKey overrides the key used for token signing (JWKS always
// advertises the original key) — used in L6 to test bad signature rejection.
type fakeIDPServer struct {
	mu         sync.Mutex
	nonce      string
	key        *rsa.PrivateKey // advertised in JWKS
	signingKey *rsa.PrivateKey // used to sign tokens; nil means use key
	server     *httptest.Server
}

// SetNonce configures the nonce embedded in the next ID token issued.
func (f *fakeIDPServer) SetNonce(nonce string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nonce = nonce
}

// SetSigningKey overrides the signing key for subsequent token responses.
// The JWKS endpoint continues to advertise the original key, so tokens
// signed with this key will fail signature verification.
func (f *fakeIDPServer) SetSigningKey(key *rsa.PrivateKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signingKey = key
}

// URL returns the base URL of the fake IdP.
func (f *fakeIDPServer) URL() string { return f.server.URL }

// newFakeIDPServer creates and starts a fake OIDC discovery/token server.
func newFakeIDPServer(t *testing.T, key *rsa.PrivateKey) *fakeIDPServer {
	t.Helper()
	f := &fakeIDPServer{key: key}
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{
			"issuer":                                f.server.URL,
			"authorization_endpoint":                f.server.URL + "/protocol/openid-connect/auth",
			"token_endpoint":                        f.server.URL + "/protocol/openid-connect/token",
			"jwks_uri":                              f.server.URL + "/protocol/openid-connect/certs",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	})

	mux.HandleFunc("/protocol/openid-connect/certs", func(w http.ResponseWriter, r *http.Request) {
		jwks := jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{{
				Key:       &key.PublicKey,
				KeyID:     "test-key-1",
				Algorithm: string(jose.RS256),
				Use:       "sig",
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})

	mux.HandleFunc("/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.FormValue("code") == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		nonce := f.nonce
		signingKey := f.signingKey
		if signingKey == nil {
			signingKey = f.key
		}
		f.mu.Unlock()
		idToken := buildFakeIDToken(t, signingKey, f.server.URL, "teleport",
			"testuser@example.com", nonce, []string{"admins"})
		resp := map[string]any{
			"access_token": "fake-access-token",
			"token_type":   "Bearer",
			"id_token":     idToken,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// oidcTestEnv is a minimal test fixture for the OIDC login service.
type oidcTestEnv struct {
	server    *auth.Server
	connector types.OIDCConnector
	idpKey    *rsa.PrivateKey
	idp       *fakeIDPServer
}

func setupOIDCTestEnv(t *testing.T) *oidcTestEnv {
	t.Helper()

	authSrv, err := authtest.NewAuthServer(authtest.AuthServerConfig{
		Dir:     t.TempDir(),
		Modules: modulestest.OSSModules(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = authSrv.AuthServer.Close() })

	srv := authSrv.AuthServer

	keySigner, err := cryptosuites.GenerateKeyWithAlgorithm(cryptosuites.RSA2048)
	require.NoError(t, err)
	idpKey := keySigner.(*rsa.PrivateKey)
	idp := newFakeIDPServer(t, idpKey)

	// Wire the OIDC login service (D6).
	srv.SetOIDCService(auth.NewOIDCService(srv))

	connector, err := types.NewOIDCConnector("keycloak", types.OIDCConnectorSpecV3{
		IssuerURL:    idp.URL(),
		ClientID:     "teleport",
		ClientSecret: "supersecret",
		RedirectURLs: []string{"https://proxy.example.com/v1/webapi/oidc/callback"},
		Scope:        []string{"openid", "profile", "email"},
		ClaimsToRoles: []types.ClaimMapping{
			{Claim: "groups", Value: "admins", Roles: []string{"access"}},
		},
	})
	require.NoError(t, err)

	return &oidcTestEnv{server: srv, connector: connector, idpKey: idpKey, idp: idp}
}

// buildFakeIDToken signs a minimal JWT ID token.
func buildFakeIDToken(t *testing.T, key *rsa.PrivateKey, issuer, audience, subject, nonce string, groups []string) string {
	t.Helper()
	sig, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key-1"),
	)
	require.NoError(t, err)
	now := time.Now()
	claims := map[string]any{
		"iss": issuer, "aud": audience, "sub": subject,
		"email": subject, "nonce": nonce,
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
		"groups": groups,
	}
	raw, err := josejwt.Signed(sig).Claims(claims).Serialize()
	require.NoError(t, err)
	return raw
}

// ─── L1: Auth request ────────────────────────────────────────────────────────

func TestOIDCL1_AuthRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := setupOIDCTestEnv(t)

	_, err := env.server.CreateOIDCConnector(ctx, env.connector)
	require.NoError(t, err)

	created, err := env.server.CreateOIDCAuthRequest(ctx, types.OIDCAuthRequest{
		ConnectorID: env.connector.GetName(), CreateWebSession: true, Type: "web",
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.StateToken)
	require.NotEmpty(t, created.RedirectURL)

	parsed, err := url.Parse(created.RedirectURL)
	require.NoError(t, err)
	q := parsed.Query()
	require.Equal(t, "teleport", q.Get("client_id"))
	require.NotEmpty(t, q.Get("state"))
	require.NotEmpty(t, q.Get("nonce"))
	require.Contains(t, q.Get("scope"), "openid")
	require.Equal(t, "code", q.Get("response_type"))
	// Nonce must equal state (used-as-nonce pattern).
	require.Equal(t, created.StateToken, q.Get("nonce"))

	// Must be persisted.
	stored, err := env.server.GetOIDCAuthRequest(ctx, created.StateToken)
	require.NoError(t, err)
	require.Equal(t, created.StateToken, stored.StateToken)
}

// ─── L2: Callback happy path ─────────────────────────────────────────────────

func TestOIDCL2_CallbackHappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := setupOIDCTestEnv(t)

	// Create the role so session creation doesn't fail.
	accessRole, err := types.NewRole("access", types.RoleSpecV6{})
	require.NoError(t, err)
	_, err = env.server.CreateRole(ctx, accessRole)
	require.NoError(t, err)

	_, err = env.server.CreateOIDCConnector(ctx, env.connector)
	require.NoError(t, err)

	created, err := env.server.CreateOIDCAuthRequest(ctx, types.OIDCAuthRequest{
		ConnectorID: env.connector.GetName(), CreateWebSession: true, Type: "web",
	})
	require.NoError(t, err)

	// Configure the fake IdP to embed the correct nonce.
	env.idp.SetNonce(created.StateToken)

	resp, err := env.server.ValidateOIDCAuthCallback(ctx, url.Values{
		"state": {created.StateToken},
		"code":  {"fake-code"},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.Username)
	require.NotNil(t, resp.Session)
	require.Equal(t, "keycloak", resp.Identity.ConnectorID)
}

// ─── L3: claims→roles ────────────────────────────────────────────────────────

func TestOIDCL3_ClaimsToRoles(t *testing.T) {
	t.Parallel()
	connector, err := types.NewOIDCConnector("kc", types.OIDCConnectorSpecV3{
		IssuerURL: "https://kc.example.com/realms/r", ClientID: "t", ClientSecret: "s",
		RedirectURLs: []string{"https://proxy.example.com/v1/webapi/oidc/callback"},
		ClaimsToRoles: []types.ClaimMapping{
			{Claim: "groups", Value: "admins", Roles: []string{"access", "editor"}},
			{Claim: "groups", Value: "viewers", Roles: []string{"reviewer"}},
		},
	})
	require.NoError(t, err)

	_, roles := services.TraitsToRoles(connector.GetTraitMappings(), map[string][]string{
		"groups": {"admins", "engineering"},
	})
	require.ElementsMatch(t, []string{"access", "editor"}, roles)
}

// ─── L4: invalid state ───────────────────────────────────────────────────────

func TestOIDCL4_InvalidState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := setupOIDCTestEnv(t)

	_, err := env.server.CreateOIDCConnector(ctx, env.connector)
	require.NoError(t, err)

	_, err = env.server.ValidateOIDCAuthCallback(ctx, url.Values{
		"state": {"this-state-does-not-exist"},
		"code":  {"fake-code"},
	})
	require.Error(t, err)
	require.True(t, trace.IsNotFound(err) || trace.IsAccessDenied(err) || trace.IsOAuth2(err),
		"expected not-found/access-denied/oauth2 error, got: %v", err)
}

// ─── L5: nonce mismatch ──────────────────────────────────────────────────────

func TestOIDCL5_NonceMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := setupOIDCTestEnv(t)

	_, err := env.server.CreateOIDCConnector(ctx, env.connector)
	require.NoError(t, err)

	created, err := env.server.CreateOIDCAuthRequest(ctx, types.OIDCAuthRequest{
		ConnectorID: env.connector.GetName(), CreateWebSession: true, Type: "web",
	})
	require.NoError(t, err)

	// Wrong nonce — service must reject.
	env.idp.SetNonce("wrong-nonce-does-not-match-state")

	_, err = env.server.ValidateOIDCAuthCallback(ctx, url.Values{
		"state": {created.StateToken},
		"code":  {"fake-code"},
	})
	require.Error(t, err)
	require.True(t, trace.IsAccessDenied(err),
		"expected access denied for nonce mismatch, got: %v", err)
}

// ─── L6: bad signature ───────────────────────────────────────────────────────

func TestOIDCL6_BadIDTokenSignature(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := setupOIDCTestEnv(t)

	_, err := env.server.CreateOIDCConnector(ctx, env.connector)
	require.NoError(t, err)

	created, err := env.server.CreateOIDCAuthRequest(ctx, types.OIDCAuthRequest{
		ConnectorID: env.connector.GetName(), CreateWebSession: false, Type: "web",
	})
	require.NoError(t, err)

	// Make the IdP sign with an unknown key that is NOT advertised in its JWKS.
	// Verification in ValidateOIDCAuthCallback must reject the token.
	unknownSigner, err := cryptosuites.GenerateKeyWithAlgorithm(cryptosuites.RSA2048)
	require.NoError(t, err)
	env.idp.SetSigningKey(unknownSigner.(*rsa.PrivateKey))
	env.idp.SetNonce(created.StateToken)

	_, err = env.server.ValidateOIDCAuthCallback(ctx, url.Values{
		"state": {created.StateToken},
		"code":  {"fake-code"},
	})
	require.Error(t, err, "token signed with unknown key must not verify")
}

// ─── L7: disallowed redirect ─────────────────────────────────────────────────

func TestOIDCL7_DisallowedClientRedirect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := setupOIDCTestEnv(t)

	_, err := env.server.CreateOIDCConnector(ctx, env.connector)
	require.NoError(t, err)

	_, err = env.server.CreateOIDCAuthRequest(ctx, types.OIDCAuthRequest{
		ConnectorID:       env.connector.GetName(),
		CreateWebSession:  false,
		ClientRedirectURL: "https://evil.example.com/callback",
		Type:              "console",
	})
	require.Error(t, err)
	require.True(t,
		trace.IsAccessDenied(err) ||
			strings.Contains(err.Error(), "invalid") ||
			strings.Contains(err.Error(), "disallowed"),
		"expected redirect-validation error, got: %v", err)
}
