package lsat

import (
	"context"
)

// ContextKey is the type that we use to identify LSAT specific values in the
// request context. We wrap the string inside a struct because of this comment
// in the context API: "The provided key must be comparable and should not be of
// type string or any other built-in type to avoid collisions between packages
// using context."
type ContextKey struct {
	Name string
}

var (
	// KeyTokenID is the key under which we store the client's token ID in
	// the request context.
	KeyTokenID = ContextKey{"tokenid"}
)

// FromContext tries to extract a value from the given context.
func FromContext(ctx context.Context, key ContextKey) interface{} {
	return ctx.Value(key)
}

// AddToContext adds the given value to the context for easy retrieval later on.
func AddToContext(ctx context.Context, key ContextKey,
	value interface{}) context.Context {

	return context.WithValue(ctx, key, value)
}
