package doctor

import (
	"context"

	"github.com/djalmajr/soho/internal/transport"
)

// listOnceRuntime answers every List of one doctor run with the first reply
// for the same scope, so the checks that read the live terminals (worker
// without a parent, finished worker left open) cost one listing together.
type listOnceRuntime struct {
	transport.Transport
	listed bool
	scope  transport.Scope
	reply  transport.Reply[[]transport.Terminal]
}

func (r *listOnceRuntime) List(ctx context.Context, scope transport.Scope, call transport.Call) transport.Reply[[]transport.Terminal] {
	if r.listed && r.scope == scope {
		return r.reply
	}
	reply := r.Transport.List(ctx, scope, call)
	if !r.listed {
		r.listed, r.scope, r.reply = true, scope, reply
	}
	return reply
}
