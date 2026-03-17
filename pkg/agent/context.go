// See LICENSE for licensing information

package agent

import "context"

type triggerContextKey struct{}

func contextWithTrigger(ctx context.Context, trigger Trigger) context.Context {
	return context.WithValue(ctx, triggerContextKey{}, trigger)
}

// TriggerFromContext returns the current shell-step trigger when one was
// attached by the agent runtime.
func TriggerFromContext(ctx context.Context) (Trigger, bool) {
	trigger, ok := ctx.Value(triggerContextKey{}).(Trigger)
	return trigger, ok
}
