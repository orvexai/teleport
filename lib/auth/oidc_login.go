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

// OIDCLoginService is the community-edition implementation of OIDCService.
// It mirrors the structure of lib/auth/github.go and uses:
//   - golang.org/x/oauth2          for PKCE verifier generation
//   - zitadel/oidc/v3              for OIDC discovery, code exchange, and ID-token verification
//
// D10 (OIDC-backed MFA) is explicitly deferred.

package auth

import (
	"context"
	"log/slog"
	"net/url"
	"time"

	"github.com/gravitational/trace"
	zrp "github.com/zitadel/oidc/v3/pkg/client/rp"
	zoidc "github.com/zitadel/oidc/v3/pkg/oidc"
	"golang.org/x/oauth2"

	"github.com/gravitational/teleport/api/constants"
	apidefaults "github.com/gravitational/teleport/api/defaults"
	"github.com/gravitational/teleport/api/types"
	apievents "github.com/gravitational/teleport/api/types/events"
	"github.com/gravitational/teleport/lib/auth/authclient"
	"github.com/gravitational/teleport/lib/authz"
	"github.com/gravitational/teleport/lib/client/sso"
	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/events"
	"github.com/gravitational/teleport/lib/loginrule"
	"github.com/gravitational/teleport/lib/services"
	"github.com/gravitational/teleport/lib/utils"
)

// OIDCLoginService implements OIDCService for the community (OSS) build.
// It is instantiated in lib/service/service.go after auth.Init returns and
// registered via Server.SetOIDCService.
type OIDCLoginService struct {
	authServer *Server
}

// NewOIDCService returns a new OIDCLoginService wired to the given auth server.
func NewOIDCService(authServer *Server) OIDCService {
	return &OIDCLoginService{authServer: authServer}
}

// newRelyingParty creates a zitadel OIDC relying party for the given connector
// and proxy address. Each call performs OIDC discovery against the issuer URL.
// Nonce validation in the library verifier is disabled (WithNonce(nil)) because
// we validate the nonce manually after code exchange for proper error typing.
func (s *OIDCLoginService) newRelyingParty(ctx context.Context, connector types.OIDCConnector, proxyAddr string) (zrp.RelyingParty, error) {
	redirectURL, err := services.GetRedirectURL(connector, proxyAddr)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	scopes := connector.GetScope()
	if len(scopes) == 0 {
		scopes = []string{zoidc.ScopeOpenID}
	}
	hasOpenID := false
	for _, sc := range scopes {
		if sc == zoidc.ScopeOpenID {
			hasOpenID = true
			break
		}
	}
	if !hasOpenID {
		scopes = append([]string{zoidc.ScopeOpenID}, scopes...)
	}

	relyingParty, err := zrp.NewRelyingPartyOIDC(ctx,
		connector.GetIssuerURL(),
		connector.GetClientID(),
		connector.GetClientSecret(),
		redirectURL,
		scopes,
		// Disable the library's built-in nonce check so we can return a typed
		// AccessDenied error for nonce mismatches rather than a generic error.
		zrp.WithVerifierOpts(zrp.WithNonce(nil)),
	)
	return relyingParty, trace.Wrap(err, "OIDC discovery for issuer %q failed", connector.GetIssuerURL())
}

// CreateOIDCAuthRequest creates a new OIDC authorization-code request.
// Mirrors Server.CreateGithubAuthRequest (github.go:142).
func (s *OIDCLoginService) CreateOIDCAuthRequest(ctx context.Context, req types.OIDCAuthRequest) (*types.OIDCAuthRequest, error) {
	connector, err := s.authServer.Services.GetOIDCConnector(ctx, req.ConnectorID, true /* withSecrets */)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Validate client redirect URL for console (non-web) flows — mirrors github.go:152.
	if !req.CreateWebSession {
		ceremonyType := sso.CeremonyTypeLogin
		if req.SSOTestFlow {
			ceremonyType = sso.CeremonyTypeTest
		}
		if err := sso.ValidateClientRedirect(req.ClientRedirectURL, ceremonyType, connector.GetClientRedirectSettings()); err != nil {
			return nil, trace.Wrap(err, InvalidClientRedirectErrorMessage)
		}
	}

	// Generate a cryptographically random state token.
	req.StateToken, err = utils.CryptoRandomHex(defaults.TokenLenBytes)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	relyingParty, err := s.newRelyingParty(ctx, connector, req.ProxyAddress)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// The state token doubles as the nonce (anti-replay). The nonce is embedded
	// in the authorization URL and later verified against the stored StateToken.
	authURLOpts := []zrp.AuthURLOpt{
		zrp.AuthURLOpt(zrp.WithURLParam("nonce", req.StateToken)),
	}

	// Honor PKCE mode from the connector (D3).
	if connector.GetPKCEMode() == constants.OIDCPKCEModeEnabled {
		verifier := oauth2.GenerateVerifier()
		req.PkceVerifier = verifier
		challenge := oauth2.S256ChallengeFromVerifier(verifier)
		authURLOpts = append(authURLOpts, zrp.WithCodeChallenge(challenge))
	}

	req.RedirectURL = zrp.AuthURL(req.StateToken, relyingParty, authURLOpts...)
	s.authServer.logger.DebugContext(ctx, "Creating OIDC auth request",
		"connector", req.ConnectorID,
		"create_web_session", req.CreateWebSession,
	)

	if err = s.authServer.Services.CreateOIDCAuthRequest(ctx, req, defaults.OIDCAuthRequestTTL); err != nil {
		return nil, trace.Wrap(err)
	}
	return &req, nil
}

// CreateOIDCAuthRequestForMFA is deferred (D10).
func (s *OIDCLoginService) CreateOIDCAuthRequestForMFA(_ context.Context, _ types.OIDCAuthRequest) (*types.OIDCAuthRequest, error) {
	return nil, trace.NotImplemented("OIDC-backed MFA is not yet supported in the community build")
}

// ValidateOIDCAuthCallback validates the OIDC callback, verifies the ID token,
// maps claims to roles, creates/updates the user, and returns an auth response.
// Mirrors Server.ValidateGithubAuthRedirect (github.go:538).
func (s *OIDCLoginService) ValidateOIDCAuthCallback(ctx context.Context, q url.Values) (*authclient.OIDCAuthResponse, error) {
	logger := s.authServer.logger.With(slog.String("connector_type", "oidc"))

	// Surface any IdP-level error.
	if errParam := q.Get("error"); errParam != "" {
		errDesc := q.Get("error_description")
		return nil, trace.WithUserMessage(
			trace.OAuth2("invalid_request", errParam, q),
			"OIDC provider returned error: %v [%v]", errDesc, errParam,
		)
	}

	code := q.Get("code")
	if code == "" {
		return nil, trace.WithUserMessage(
			trace.OAuth2("invalid_request", "code query param must be set", q),
			"Invalid parameters received from OIDC provider.",
		)
	}

	stateToken := q.Get("state")
	if stateToken == "" {
		return nil, trace.WithUserMessage(
			trace.OAuth2("invalid_request", "missing state query param", q),
			"Invalid parameters received from OIDC provider.",
		)
	}

	// Load the stored request by state (validates state). [D2]
	req, err := s.authServer.Services.GetOIDCAuthRequest(ctx, stateToken)
	if err != nil {
		return nil, trace.Wrap(err, "Failed to get OIDC auth request.")
	}

	connector, err := s.authServer.Services.GetOIDCConnector(ctx, req.ConnectorID, true /* withSecrets */)
	if err != nil {
		return nil, trace.Wrap(err, "Failed to get OIDC connector.")
	}

	relyingParty, err := s.newRelyingParty(ctx, connector, req.ProxyAddress)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Build PKCE exchange option if a verifier is stored. [D3]
	var codeExchangeOpts []zrp.CodeExchangeOpt
	if req.PkceVerifier != "" {
		codeExchangeOpts = append(codeExchangeOpts, zrp.WithCodeVerifier(req.PkceVerifier))
	}

	// Exchange authorization code for tokens; ID token signature is verified
	// against the provider JWKS during the exchange. [D2]
	tokens, err := zrp.CodeExchange[*zoidc.IDTokenClaims](ctx, code, relyingParty, codeExchangeOpts...)
	if err != nil {
		return nil, trace.Wrap(err, "OIDC code exchange failed.")
	}

	// Validate nonce: ID token nonce must match the stored state token. [D2]
	if tokens.IDTokenClaims.GetNonce() != req.StateToken {
		return nil, trace.AccessDenied("OIDC nonce mismatch — possible replay attack")
	}

	subject := tokens.IDTokenClaims.GetSubject()
	claims := tokens.IDTokenClaims.Claims // map[string]any with non-standard claims

	logger.DebugContext(ctx, "Validated OIDC ID token",
		"subject", subject,
		"connector", req.ConnectorID,
	)

	// Map claims → traits → roles. [D8]
	traits := oidcClaimsToTraits(claims)
	_, roles := services.TraitsToRoles(connector.GetTraitMappings(), traits)

	if len(roles) == 0 {
		return nil, trace.AccessDenied(
			"no Teleport roles were mapped from OIDC claims for connector %q; check claims_to_roles configuration",
			req.ConnectorID,
		)
	}

	// Apply login rules.
	evaluationInput := &loginrule.EvaluationInput{Traits: traits}
	evaluationOutput, err := s.authServer.GetLoginRuleEvaluator().Evaluate(ctx, evaluationInput)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	traits = evaluationOutput.Traits
	// Recompute roles after login rule evaluation.
	_, roles = services.TraitsToRoles(connector.GetTraitMappings(), traits)
	if len(roles) == 0 {
		return nil, trace.AccessDenied(
			"no Teleport roles remain after applying login rules for OIDC connector %q",
			req.ConnectorID,
		)
	}

	// Resolve username: use the configured username_claim if set, otherwise fall back to sub.
	// (Used below for session TTL calculation and user params.)
	username := subject
	if claimName := connector.GetUsernameClaim(); claimName != "" {
		if v, ok := claims[claimName]; ok {
			if s, ok := v.(string); ok && s != "" {
				username = s
			}
		}
	}

	// Calculate session TTL.
	fetchedRoles, err := services.FetchRolesWithContext(roles, s.authServer, services.RoleTemplateContext{
		Username: username,
		Traits:   traits,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	roleTTL := fetchedRoles.AdjustSessionTTL(apidefaults.MaxCertDuration)
	sessionTTL := utils.MinTTL(roleTTL, req.CertTTL)

	// Build user params.
	p := &oidcUserParams{
		connectorName: req.ConnectorID,
		username:      username,
		roles:         roles,
		traits:        traits,
		sessionTTL:    sessionTTL,
	}

	// In test flow skip signing and creating web sessions.
	if req.SSOTestFlow {
		return &authclient.OIDCAuthResponse{
			Username: p.username,
			Identity: types.ExternalIdentity{
				ConnectorID: p.connectorName,
				Username:    p.username,
			},
			Req: oidcAuthRequestToClient(req),
		}, nil
	}

	user, err := s.createOIDCUser(ctx, p)
	if err != nil {
		return nil, trace.Wrap(err, "Failed to create user from OIDC claims.")
	}

	if err := s.authServer.CallLoginHooks(ctx, user); err != nil {
		return nil, trace.Wrap(err)
	}

	userState, err := s.authServer.GetUserOrLoginState(ctx, user.GetName())
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return s.makeOIDCAuthResponse(ctx, req, userState, p.sessionTTL)
}

// oidcUserParams carries computed user attributes between helper functions.
type oidcUserParams struct {
	connectorName string
	username      string
	roles         []string
	traits        map[string][]string
	sessionTTL    time.Duration
}

// makeOIDCAuthResponse mirrors makeGithubAuthResponse (github.go:661). [D9]
func (s *OIDCLoginService) makeOIDCAuthResponse(
	ctx context.Context,
	req *types.OIDCAuthRequest,
	userState services.UserState,
	sessionTTL time.Duration,
) (*authclient.OIDCAuthResponse, error) {
	auth := authclient.OIDCAuthResponse{
		Username: userState.GetName(),
		Identity: types.ExternalIdentity{
			ConnectorID: req.ConnectorID,
			Username:    userState.GetName(),
		},
		Req: oidcAuthRequestToClient(req),
	}

	if req.CreateWebSession {
		session, err := s.authServer.CreateWebSessionFromReq(ctx, NewWebSessionRequest{
			User:                 userState.GetName(),
			Roles:                userState.GetRoles(),
			Traits:               userState.GetTraits(),
			SessionTTL:           sessionTTL,
			LoginTime:            s.authServer.GetClock().Now().UTC(),
			LoginIP:              req.ClientLoginIP,
			LoginUserAgent:       req.ClientUserAgent,
			AttestWebSession:     true,
			CreateDeviceWebToken: true,
			Scope:                req.Scope,
		})
		if err != nil {
			return nil, trace.Wrap(err, "Failed to create web session.")
		}
		auth.Session = session
	}

	if len(req.SshPublicKey) != 0 || len(req.TlsPublicKey) != 0 {
		sshCert, tlsCert, err := s.authServer.CreateSessionCerts(ctx, &SessionCertsRequest{
			UserState:         userState,
			SessionTTL:        sessionTTL,
			SSHPubKey:         req.SshPublicKey,
			TLSPubKey:         req.TlsPublicKey,
			Compatibility:     req.Compatibility,
			RouteToCluster:    req.RouteToCluster,
			KubernetesCluster: req.KubernetesCluster,
			LoginIP:           req.ClientLoginIP,
			Scope:             req.Scope,
		})
		if err != nil {
			return nil, trace.Wrap(err, "Failed to create session certificate.")
		}

		clusterName, err := s.authServer.GetClusterName(ctx)
		if err != nil {
			return nil, trace.Wrap(err, "Failed to obtain cluster name.")
		}

		auth.Cert = sshCert
		auth.TLSCert = tlsCert

		authority, err := s.authServer.GetCertAuthority(ctx, types.CertAuthID{
			Type:       types.HostCA,
			DomainName: clusterName.GetClusterName(),
		}, false)
		if err != nil {
			return nil, trace.Wrap(err, "Failed to obtain cluster's host CA.")
		}
		auth.HostSigners = append(auth.HostSigners, authority)
	}

	if o, err := s.authServer.ClientOptionsForLogin(userState); err == nil {
		auth.ClientOptions = o
	} else {
		s.authServer.logger.WarnContext(ctx, "Failed to calculate client options for OIDC login",
			"username", userState.GetName(), "error", err)
	}

	return &auth, nil
}

// createOIDCUser creates or updates a Teleport user from OIDC claims.
// Mirrors createGithubUser (github.go:954). [D9]
func (s *OIDCLoginService) createOIDCUser(ctx context.Context, p *oidcUserParams) (types.User, error) {
	s.authServer.logger.DebugContext(ctx, "Generating dynamic OIDC identity",
		"connector_name", p.connectorName,
		"user_name", p.username,
		"roles", p.roles,
	)

	expires := s.authServer.GetClock().Now().UTC().Add(p.sessionTTL)
	user := &types.UserV2{
		Kind:    types.KindUser,
		Version: types.V2,
		Metadata: types.Metadata{
			Name:      p.username,
			Namespace: apidefaults.Namespace,
			Expires:   &expires,
		},
		Spec: types.UserSpecV2{
			Roles:  p.roles,
			Traits: p.traits,
			OIDCIdentities: []types.ExternalIdentity{{
				ConnectorID: p.connectorName,
				Username:    p.username,
			}},
			CreatedBy: types.CreatedBy{
				User: types.UserRef{Name: "system"},
				Time: s.authServer.GetClock().Now().UTC(),
				Connector: &types.ConnectorRef{
					Type:     constants.OIDC,
					ID:       p.connectorName,
					Identity: p.username,
				},
			},
		},
	}

	existingUser, err := s.authServer.Services.GetUser(ctx, p.username, false)
	if err != nil && !trace.IsNotFound(err) {
		return nil, trace.Wrap(err)
	}

	if existingUser != nil {
		ref := user.GetCreatedBy().Connector
		if !ref.IsSameProvider(existingUser.GetCreatedBy().Connector) {
			return nil, trace.AlreadyExists("local user %q already exists and is not an OIDC user",
				existingUser.GetName())
		}
		user.SetRevision(existingUser.GetRevision())
		if _, err := s.authServer.UpdateUser(ctx, user); err != nil {
			return nil, trace.Wrap(err)
		}
	} else {
		if _, err := s.authServer.CreateUser(ctx, user); err != nil {
			return nil, trace.Wrap(err)
		}
	}

	return user, nil
}

// oidcClaimsToTraits converts OIDC ID-token extra claims (map[string]any) to
// the Teleport trait format expected by TraitsToRoles.
// String claim values are wrapped in a slice; []any values with string elements
// are extracted as-is; other types are skipped.
func oidcClaimsToTraits(claims map[string]any) map[string][]string {
	traits := make(map[string][]string, len(claims))
	for k, v := range claims {
		switch val := v.(type) {
		case string:
			traits[k] = []string{val}
		case []any:
			var strs []string
			for _, elem := range val {
				if s, ok := elem.(string); ok {
					strs = append(strs, s)
				}
			}
			if len(strs) > 0 {
				traits[k] = strs
			}
		}
	}
	return traits
}

// oidcAuthRequestToClient converts the proto OIDCAuthRequest to the client
// representation used inside OIDCAuthResponse.
func oidcAuthRequestToClient(req *types.OIDCAuthRequest) authclient.OIDCAuthRequest {
	return authclient.OIDCAuthRequest{
		ConnectorID:       req.ConnectorID,
		CSRFToken:         req.CSRFToken,
		CreateWebSession:  req.CreateWebSession,
		ClientRedirectURL: req.ClientRedirectURL,
		SSHPubKey:         req.SshPublicKey,
		TLSPubKey:         req.TlsPublicKey,
	}
}

// validateOIDCAuthCallbackHelper emits the SSO login audit event and delegates
// to OIDCService.ValidateOIDCAuthCallback. It mirrors
// validateGithubAuthCallbackHelper (github.go:438). [D11]
func validateOIDCAuthCallbackHelper(
	ctx context.Context,
	svc OIDCService,
	q url.Values,
	emitter apievents.Emitter,
	logger *slog.Logger,
) (*authclient.OIDCAuthResponse, error) {
	event := &apievents.UserLogin{
		Metadata: apievents.Metadata{
			Type: events.UserLoginEvent,
		},
		Method:             events.LoginMethodOIDC,
		ConnectionMetadata: authz.ConnectionMetadata(ctx),
	}

	auth, err := svc.ValidateOIDCAuthCallback(ctx, q)
	if err != nil {
		event.Code = events.UserSSOLoginFailureCode
		event.Status.Success = false
		event.Status.Error = trace.Unwrap(err).Error()
		event.Status.UserMessage = err.Error()
		if emitErr := emitter.EmitAuditEvent(ctx, event); emitErr != nil {
			logger.WarnContext(ctx, "Failed to emit OIDC login failed event", "error", emitErr)
		}
		return nil, trace.Wrap(err)
	}

	event.Code = events.UserSSOLoginCode
	event.Status.Success = true
	event.User = auth.Username
	if emitErr := emitter.EmitAuditEvent(ctx, event); emitErr != nil {
		logger.WarnContext(ctx, "Failed to emit OIDC login event", "error", emitErr)
	}
	return auth, nil
}
