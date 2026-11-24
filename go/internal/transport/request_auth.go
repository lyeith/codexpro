package transport

import (
	"context"
	"net/http"
	"slices"

	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type requestAuthorizationKey struct{}

const requestAuthorizationExtraKey = "codexpro.requestAuthorization"

// requestAuthorization is the immutable decision made by ServeHTTP for this
// request. It carries no bearer credential and is never accepted from headers,
// JSON params or the SDK session's initialized context.
type requestAuthorization struct {
	identity wire.Identity
	projects []string
}

func withRequestAuthorization(ctx context.Context, identity wire.Identity, projects []string) context.Context {
	decision := &requestAuthorization{identity: identity, projects: slices.Clone(projects)}
	return context.WithValue(wire.WithIdentity(ctx, identity), requestAuthorizationKey{}, decision)
}

// sdkAuthenticatedHandler adapts the already admitted HTTP request into the
// SDK's supported TokenInfo channel. The SDK copies it to RequestExtra for each
// POST, even though method contexts otherwise inherit initialize's identity.
func sdkAuthenticatedHandler(next http.Handler) http.Handler {
	verifier := func(ctx context.Context, _ string, _ *http.Request) (*auth.TokenInfo, error) {
		decision, ok := ctx.Value(requestAuthorizationKey{}).(*requestAuthorization)
		if !ok || decision == nil || decision.identity.Source == "" {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{Extra: map[string]any{requestAuthorizationExtraKey: decision}}, nil
	}
	// These opaque policy credentials have no independent expiry claim. Their
	// revocation, identity and grants were checked at this HTTP admission.
	admit := auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The SDK's bearer lexer is narrower than admitted legacy credentials
		// (which may contain internal whitespace). Give only that lexer a
		// marker; the private admission decision remains the verifier's proof.
		// Restore the original headers before the SDK or logical mail sees them.
		headers := r.Header
		lexical := r.WithContext(r.Context())
		lexical.Header = http.Header{"Authorization": {"Bearer codexpro-admitted-request"}}
		restore := http.HandlerFunc(func(w http.ResponseWriter, admitted *http.Request) {
			admitted.Header = headers
			next.ServeHTTP(w, admitted)
		})
		admit(restore).ServeHTTP(w, lexical)
	})
}

func authorizationFromExtra(extra *mcp.RequestExtra) (*requestAuthorization, bool) {
	if extra == nil || extra.TokenInfo == nil {
		return nil, false
	}
	decision, ok := extra.TokenInfo.Extra[requestAuthorizationExtraKey].(*requestAuthorization)
	return decision, ok && decision != nil && decision.identity.Source != ""
}
