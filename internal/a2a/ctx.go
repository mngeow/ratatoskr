package a2a

import "context"

type ctxKey int

const (
	callerKey ctxKey = iota
	targetKey
)

// WithCaller puts the authenticated principal in a request context. Only the handler wrapper calls it,
// after authentication; the SDK's own User/Authenticator is left unset so scoping has one source.
func WithCaller(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, callerKey, p)
}

// CallerFrom returns the authenticated principal of a request context.
func CallerFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(callerKey).(Principal)
	return p, ok && p.Valid()
}

// WithTarget puts the resolved participant a request is addressed to in the context.
func WithTarget(ctx context.Context, t Target) context.Context {
	return context.WithValue(ctx, targetKey, t)
}

// TargetFrom returns the resolved target of a request context.
func TargetFrom(ctx context.Context) (Target, bool) {
	t, ok := ctx.Value(targetKey).(Target)
	return t, ok && t.ID != ""
}
