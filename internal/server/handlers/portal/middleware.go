package portal

import (
	"context"
	"net/http"

	"github.com/sqoia-dev/clustr/internal/db"
)

// ViewerMiddleware enriches the request context with the viewer's LDAP UID.
// It reads the clustr user ID from the parent middleware's context, looks up the
// UserRecord (to get the username = LDAP UID), and sets both in context values
// that the portal Handler methods consume.
//
// This must be placed after the server-level apiKeyAuth middleware so that the
// user ID is already in context. It is a no-op (passes through) when no user
// session is active (API key path — not expected on /portal/ routes, but safe).
func ViewerMiddleware(database *db.DB, userIDFromCtx func(context.Context) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			userID := userIDFromCtx(ctx)
			if userID != "" {
				// Look up the user record to get the username (= LDAP UID).
				user, err := database.GetUser(ctx, userID)
				if err == nil {
					// Set both the clustr user ID and the LDAP UID in context.
					ctx = context.WithValue(ctx, ctxKeyPortalUID{}, userID)
					ctx = context.WithValue(ctx, ctxKeyPortalLDAPUID{}, user.Username)
				}
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// PIMiddleware enriches the request context for the PI portal:
//   - sets ctxKeyPortalUID (clustr user ID)
//   - sets ctxKeyPIRole (role string: "pi" or "admin") for PI-scoped auth checks
//
// This must run after the server-level apiKeyAuth middleware.
// userIDFromCtx and roleFromCtx are closures from the server package that read
// the respective context keys set by apiKeyAuth.
func PIMiddleware(
	database *db.DB,
	userIDFromCtx func(context.Context) string,
	roleFromCtx func(context.Context) string,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			userID := userIDFromCtx(ctx)
			role := roleFromCtx(ctx)
			if userID != "" {
				ctx = context.WithValue(ctx, ctxKeyPortalUID{}, userID)
				ctx = context.WithValue(ctx, ctxKeyPIRole{}, role)
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
