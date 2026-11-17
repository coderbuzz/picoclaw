package mcp

import "context"

// CallerMetaKey is the _meta key that carries who is asking (chat, sender, agent) on every tools/call.
// The bot sets it from the inbound message, never from model arguments, so an app may trust it as far as it
// trusts the bot's token.
const CallerMetaKey = "id.qumatic/caller"

type callMetaKey struct{}

// WithCallerMeta attaches caller facts to ctx; CallTool sends them as params._meta[CallerMetaKey].
func WithCallerMeta(ctx context.Context, caller map[string]string) context.Context {
	return context.WithValue(ctx, callMetaKey{}, caller)
}

func callMeta(ctx context.Context) map[string]any {
	caller, _ := ctx.Value(callMetaKey{}).(map[string]string)
	if len(caller) == 0 {
		return nil
	}
	return map[string]any{CallerMetaKey: caller}
}
