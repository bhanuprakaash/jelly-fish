package auth

import "context"

type userKey struct{}

// WithUser returns ctx carrying u, as set by the Authn middleware.
func WithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

// UserFrom returns the User WithUser stored in ctx.
func UserFrom(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userKey{}).(User)
	return u, ok
}
