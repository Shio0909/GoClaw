package tools

import "context"

// Pusher allows tools to proactively send messages to the user who triggered
// the current conversation, without waiting for a user reply.
// Gateways (QQ, Feishu, …) inject a concrete Pusher into the context before
// calling agent.Run so that tools like the reminder can deliver async messages.
type Pusher interface {
	Push(ctx context.Context, msg string) error
}

type pusherKeyType struct{}

var pusherKey pusherKeyType

// WithPusher attaches a Pusher to the context.
func WithPusher(ctx context.Context, p Pusher) context.Context {
	return context.WithValue(ctx, pusherKey, p)
}

// GetPusher retrieves the Pusher from the context.
// Returns (nil, false) if no Pusher has been injected.
func GetPusher(ctx context.Context) (Pusher, bool) {
	p, ok := ctx.Value(pusherKey).(Pusher)
	return p, ok
}
