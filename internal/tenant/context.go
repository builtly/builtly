package tenant

import "context"

type ctxKey struct{}

// WithApp attaches the resolved app to ctx.
func WithApp(ctx context.Context, app App) context.Context {
	return context.WithValue(ctx, ctxKey{}, app)
}

// FromContext returns the app previously attached by middleware.
func FromContext(ctx context.Context) (App, bool) {
	app, ok := ctx.Value(ctxKey{}).(App)
	return app, ok
}
