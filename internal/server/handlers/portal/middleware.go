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
