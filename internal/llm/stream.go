// stream.go — helpers shared by the protocol adapters: channel sizing, event
// delivery against ctx, and tool-argument parsing.
package llm

import (
	"context"
	"encoding/json"
	"strings"
)

// channelBuffer sizes the stream event channels: big enough to absorb a delta
// burst without stalling the SSE reader.
const channelBuffer = 64

// forwardEvent delivers ev to ch, giving up when ctx is cancelled. False
// means the event was not delivered: stop reading and release the connection.
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
// select would pick randomly when the buffer has room AND ctx is already done,
// silently dropping the terminal event on cancelled streams.
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

// parseToolArgs decodes an accumulated tool-use JSON string into a map;
// malformed (e.g. truncated) input yields an empty map rather than failing the
// stream.
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
