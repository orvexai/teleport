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
// All cases are skipped (t.Skip) until Wave 2 un-skips them alongside the
// implementation.  The file compiles in Wave 0 to keep the gate green.

package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/auth"
	"github.com/gravitational/teleport/lib/auth/authtest"
	authority "github.com/gravitational/teleport/lib/auth/testauthority"
	"github.com/gravitational/teleport/lib/backend/memory"
	"github.com/gravitational/teleport/lib/modules"
	"github.com/gravitational/teleport/lib/modules/modulestest"
	"github.com/gravitational/teleport/lib/services"
)

// oidcTestEnv is a minimal test fixture for the OIDC login service.
type oidcTestEnv struct {
	server    *auth.Server
	connector types.OIDCConnector
	// idpKey is the private key used by the fake IdP to sign ID tokens.
	idpKey *rsa.PrivateKey
	// idpServer is the fake OIDC discovery/token server.
	idpServer *httptest.Server
}

// setupOIDCTestEnv creates a minimal auth server and a fake Keycloak-like OIDC
// IdP for unit-testing the OIDC login service.
func setupOIDCTestEnv(t *testing.T) *oidcTestEnv {
	t.Helper()

	clk := clockwork.NewFakeClockAt(time.Now())

	b, err := memory.New(memory.Config{
		Context: t.Context(),
		Clock:   clk,
	})
	require.NoError(t, err)

	clusterName, err := services.NewClusterNameWithRandomID(types.ClusterNameSpecV2{
		ClusterName: "oidc-test.localhost",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })

	keygen, err := authority.NewKeygen(modules.BuildOSS, clk.Now)
	require.NoError(t, err)

	// Use OSS modules with OIDC enabled so the entitlement gate passes.
	ossModules := modulestest.OSSModules()

	srv, err := auth.NewServer(&auth.InitConfig{
		ClusterName:            clusterName,
		Backend:                b,
		VersionStorage:         authtest.NewFakeTeleportVersion(),
		Authority:              keygen,
		SkipPeriodicOperations: true,
		HostUUID:               uuid.NewString(),
		Modules:                ossModules,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	// Generate an RSA key for the fake IdP.
	idpKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Build a fake OIDC discovery server.
	idpServer := buildFakeOIDCServer(t, idpKey)

	// Create the connector pointing at the fake IdP.
	connector, err := types.NewOIDCConnector("keycloak", types.OIDCConnectorSpecV3{
		IssuerURL:    idpServer.URL,
		ClientID:     "teleport",
		ClientSecret: "supersecret",
		RedirectURLs: []string{"https://proxy.example.com/v1/webapi/oidc/callback"},
		Scope:        []string{"openid", "profile", "email"},
		ClaimsToRoles: []types.ClaimMapping{
			{
				Claim: "groups",
				Value: "admins",
				Roles: []string{"access"},
			},
		},
	})
	require.NoError(t, err)

	return &oidcTestEnv{
		server:    srv,
		connector: connector,
		idpKey:    idpKey,
		idpServer: idpServer,
	}
}

// buildFakeOIDCServer returns an httptest.Server that serves OIDC discovery,
// JWKS, and a minimal token endpoint — enough for unit tests.
func buildFakeOIDCServer(t *testing.T, key *rsa.PrivateKey) *httptest.Server {
	t.Helper()

	var srv *httptest.Server
	mux := http.NewServeMux()

	// Discovery document.
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/protocol/openid-connect/auth",
			"token_endpoint":                        srv.URL + "/protocol/openid-connect/token",
			"jwks_uri":                              srv.URL + "/protocol/openid-connect/certs",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	})

	// JWKS endpoint.
	mux.HandleFunc("/protocol/openid-connect/certs", func(w http.ResponseWriter, r *http.Request) {
		jwks := jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{
				{
					Key:       &key.PublicKey,
					KeyID:     "test-key-1",
					Algorithm: string(jose.RS256),
					Use:       "sig",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})

	// Token endpoint — returns a signed ID token for testing.
	mux.HandleFunc("/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		nonce := r.FormValue("nonce")
		code := r.FormValue("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		idToken := buildFakeIDToken(t, key, srv.URL, "teleport", "testuser@example.com", nonce, []string{"admins"})
		resp := map[string]any{
			"access_token": "fake-access-token",
			"token_type":   "Bearer",
			"id_token":     idToken,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// buildFakeIDToken signs a minimal JWT ID token with the given RSA key.
func buildFakeIDToken(t *testing.T, key *rsa.PrivateKey, issuer, audience, subject, nonce string, groups []string) string {
	t.Helper()

	sig, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key-1"),
	)
	require.NoError(t, err)

	now := time.Now()
	claims := map[string]any{
		"iss":    issuer,
		"aud":    audience,
		"sub":    subject,
		"email":  subject,
		"nonce":  nonce,
		"iat":    now.Unix(),
		"exp":    now.Add(10 * time.Minute).Unix(),
		"groups": groups,
	}

	raw, err := josejwt.Signed(sig).Claims(claims).Serialize()
	require.NoError(t, err)
	return raw
}

// buildFakeIDTokenWithKey signs with a different key to simulate a bad
// signature.
func buildFakeIDTokenWithKey(t *testing.T, key *rsa.PrivateKey, issuer, audience, subject, nonce string) string {
	t.Helper()
	sig, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "wrong-key"),
	)
	require.NoError(t, err)

	now := time.Now()
	claims := map[string]any{
		"iss":   issuer,
		"aud":   audience,
		"sub":   subject,
		"nonce": nonce,
		"iat":   now.Unix(),
		"exp":   now.Add(10 * time.Minute).Unix(),
	}
	raw, err := josejwt.Signed(sig).Claims(claims).Serialize()
	require.NoError(t, err)
	return raw
}

// ─── L1: Auth request ────────────────────────────────────────────────────────

// TestOIDCL1_AuthRequest verifies that createOIDCAuthRequest builds a valid
// authorization-code URL (issuer's authorize endpoint, client_id, scope incl.
// openid, state, nonce) and persists the request.
func TestOIDCL1_AuthRequest(t *testing.T) {
	t.Skip("un-skip in Wave 2")

	ctx := context.Background()
	env := setupOIDCTestEnv(t)

	// Ensure the connector exists in storage.
	_, err := env.server.CreateOIDCConnector(ctx, env.connector)
	require.NoError(t, err)

	req := types.OIDCAuthRequest{
		ConnectorID:      env.connector.GetName(),
		CreateWebSession: true,
		Type:             "web",
	}

	created, err := env.server.CreateOIDCAuthRequest(ctx, req)
	require.NoError(t, err)
	require.NotEmpty(t, created.StateToken, "state token must be set")
	require.NotEmpty(t, created.RedirectURL, "redirect URL must be set")

	parsed, err := url.Parse(created.RedirectURL)
	require.NoError(t, err)

	q := parsed.Query()
	require.Equal(t, "teleport", q.Get("client_id"))
	require.NotEmpty(t, q.Get("state"))
	require.NotEmpty(t, q.Get("nonce"))
	require.Contains(t, q.Get("scope"), "openid")
	require.Equal(t, "code", q.Get("response_type"))

	// Must be persisted.
	stored, err := env.server.GetOIDCAuthRequest(ctx, created.StateToken)
	require.NoError(t, err)
	require.Equal(t, created.StateToken, stored.StateToken)
}

// ─── L2: Callback happy path ─────────────────────────────────────────────────

// TestOIDCL2_CallbackHappyPath verifies that a well-formed callback creates /
// updates the user, applies claims→roles, and returns a populated
// OIDCAuthResponse with a session.
func TestOIDCL2_CallbackHappyPath(t *testing.T) {
	t.Skip("un-skip in Wave 2")

	ctx := context.Background()
	env := setupOIDCTestEnv(t)

	_, err := env.server.CreateOIDCConnector(ctx, env.connector)
	require.NoError(t, err)

	// Create an auth request and capture state/nonce so the fake token
	// endpoint can embed them.
	req := types.OIDCAuthRequest{
		ConnectorID:      env.connector.GetName(),
		CreateWebSession: true,
		Type:             "web",
	}
	created, err := env.server.CreateOIDCAuthRequest(ctx, req)
	require.NoError(t, err)

	q := url.Values{}
	q.Set("state", created.StateToken)
	q.Set("code", "fake-code")

	resp, err := env.server.ValidateOIDCAuthCallback(ctx, q)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.Username)
	require.NotNil(t, resp.Session)
}

// ─── L3: claims→roles ────────────────────────────────────────────────────────

// TestOIDCL3_ClaimsToRoles verifies that connector ClaimsToRoles maps a
// Keycloak groups claim to the expected Teleport roles via services.TraitsToRoles.
func TestOIDCL3_ClaimsToRoles(t *testing.T) {
	t.Skip("un-skip in Wave 2")

	connector, err := types.NewOIDCConnector("kc", types.OIDCConnectorSpecV3{
		IssuerURL:    "https://keycloak.example.com/realms/myrealm",
		ClientID:     "teleport",
		ClientSecret: "s",
		ClaimsToRoles: []types.ClaimMapping{
			{Claim: "groups", Value: "admins", Roles: []string{"access", "editor"}},
			{Claim: "groups", Value: "viewers", Roles: []string{"reviewer"}},
		},
	})
	require.NoError(t, err)

	traits := map[string][]string{
		"groups": {"admins", "engineering"},
	}
	_, roles := services.TraitsToRoles(connector.GetTraitMappings(), traits)
	require.ElementsMatch(t, []string{"access", "editor"}, roles)
}

// ─── L4: invalid/missing state token ─────────────────────────────────────────

// TestOIDCL4_InvalidState verifies that an invalid or missing state token is
// rejected before any user or session is created.
func TestOIDCL4_InvalidState(t *testing.T) {
	t.Skip("un-skip in Wave 2")

	ctx := context.Background()
	env := setupOIDCTestEnv(t)

	_, err := env.server.CreateOIDCConnector(ctx, env.connector)
	require.NoError(t, err)

	q := url.Values{}
	q.Set("state", "this-state-does-not-exist")
	q.Set("code", "fake-code")

	_, err = env.server.ValidateOIDCAuthCallback(ctx, q)
	require.Error(t, err)
	require.True(t, trace.IsNotFound(err) || trace.IsAccessDenied(err) || trace.IsOAuth2(err),
		"expected a not-found/access-denied/oauth2 error, got: %v", err)
}

// ─── L5: nonce mismatch ───────────────────────────────────────────────────────

// TestOIDCL5_NonceMismatch verifies that a nonce mismatch in the ID token is
// rejected.
func TestOIDCL5_NonceMismatch(t *testing.T) {
	t.Skip("un-skip in Wave 2")

	ctx := context.Background()
	env := setupOIDCTestEnv(t)

	_, err := env.server.CreateOIDCConnector(ctx, env.connector)
	require.NoError(t, err)

	// Use a real OIDC discovery to get a valid provider; build a token with a
	// wrong nonce.
	//
	// The fake token server returns a nonce-embedded token.  We cannot easily
	// inject a wrong nonce into the server's response here without extra
	// machinery, so this test drives the service with a deliberately mutated
	// callback that triggers nonce validation.
	//
	// The implementation must store the nonce inside the OIDCAuthRequest and
	// compare it against the nonce claim in the verified ID token.
	_ = ctx

	// This test requires the real service to be wired; the concrete assertion
	// is added in Wave 2.
	t.Skip("un-skip in Wave 2 once the nonce is stored in OIDCAuthRequest")
}

// ─── L6: bad ID-token signature ──────────────────────────────────────────────

// TestOIDCL6_BadIDTokenSignature verifies that an ID token signed with an
// unknown key is rejected.
func TestOIDCL6_BadIDTokenSignature(t *testing.T) {
	t.Skip("un-skip in Wave 2")

	// Generate a separate key that is NOT registered in the fake IdP's JWKS.
	unknownKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Build a bad token signed with the unknown key.
	env := setupOIDCTestEnv(t)
	badToken := buildFakeIDTokenWithKey(t, unknownKey, env.idpServer.URL, "teleport", "user@example.com", "nonce123")
	require.NotEmpty(t, badToken)

	// The verifier must reject this token; confirmed by the implementation in Wave 2.
	ctx := context.Background()
	provider, err := oidc.NewProvider(ctx, env.idpServer.URL)
	require.NoError(t, err)

	verifier := provider.Verifier(&oidc.Config{ClientID: "teleport", SkipExpiryCheck: true})
	_, err = verifier.Verify(ctx, badToken)
	require.Error(t, err, "token signed with unknown key must not verify")
}

// ─── L7: disallowed client redirect URL ──────────────────────────────────────

// TestOIDCL7_DisallowedClientRedirect verifies that sso.ValidateClientRedirect
// rejects a disallowed client redirect URL, mirroring github.go:159.
func TestOIDCL7_DisallowedClientRedirect(t *testing.T) {
	t.Skip("un-skip in Wave 2")

	ctx := context.Background()
	env := setupOIDCTestEnv(t)

	_, err := env.server.CreateOIDCConnector(ctx, env.connector)
	require.NoError(t, err)

	// A console-mode auth request with an arbitrary disallowed redirect URL.
	req := types.OIDCAuthRequest{
		ConnectorID:       env.connector.GetName(),
		CreateWebSession:  false, // console flow — redirect URL is validated
		ClientRedirectURL: "https://evil.example.com/callback",
		Type:              "console",
	}

	_, err = env.server.CreateOIDCAuthRequest(ctx, req)
	require.Error(t, err)
	require.True(t, trace.IsAccessDenied(err) || strings.Contains(err.Error(), "invalid") ||
		strings.Contains(err.Error(), "disallowed"),
		"expected a redirect-validation error, got: %v", err)
}
