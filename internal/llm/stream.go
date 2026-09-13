// stream.go — helpers shared by the protocol adapters: channel sizing, event
// delivery against ctx, and tool-argument parsing. Everything here is
// protocol-agnostic; per-adapter translation stays in the adapter files.
package llm

import (
	"context"
	"encoding/json"
	"strings"
)

// channelBuffer sizes the stream event channels. Large enough to hold a
// burst of deltas so a momentarily slow consumer never stalls the SSE
// reader; the reader also never blocks indefinitely because delivery
// consults ctx.
const channelBuffer = 64

// forwardEvent delivers ev to ch, giving up when ctx is cancelled. It
// returns false when the event was not delivered — the signal for the
// adapter loop to stop reading and release the connection.
func forwardEvent(ctx context.Context, ch chan<- StreamEvent, ev StreamEvent) bool {
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// finishEvent delivers the terminal event (StreamEnd or StreamError). The
// two-step select encodes a priority: delivery beats cancellation. A plain
// select over both cases would pick randomly when the buffer has room AND
// ctx is already done, dropping the terminal event on cancelled streams.
// Step one takes the buffer fast path without consulting ctx; only a full
// buffer falls through to step two, where a live ctx blocks until the
// consumer takes the event (the contract "exactly one terminal event before
// close" holds for any draining consumer) and a cancelled ctx releases the
// send — the consumer is unwinding on cancel anyway.
func finishEvent(ctx context.Context, ch chan<- StreamEvent, ev StreamEvent) {
	select {
	case ch <- ev:
		return
	default:
	}
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}

// parseToolArgs decodes an accumulated tool-use JSON string into a map.
// Malformed (e.g. truncated) input yields an empty map rather than failing
// the stream.
func parseToolArgs(raw string) map[string]any {
	args := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return args
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return map[string]any{}
	}
	return args
}
