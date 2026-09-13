package conversation

// Anthropic rejects any request where a tool_use has no paired tool_result.
// Within one process the agent keeps the history pair-complete itself (every
// announced call is committed with a result, executed or synthesized), so a
// clean history needs no repair. Unpaired history still reaches a request
// boundary once sessions are restored from disk or history is assembled by
// other means; the synthesized results below tell the model the tool
// produced nothing trustworthy.
const (
	// InterruptedToolResult stands in for tool calls that never got a result.
	// The tool may not have started, or may have been cut short, so the
	// wording must not claim it had no effects.
	InterruptedToolResult = "Tool execution was interrupted. The tool may or may not have completed; verify before relying on its effects."
)

// EnsureToolPairing returns messages with the tool_use/tool_result pairing
// repaired:
//   - tool_use blocks with no result get a synthesized error tool_result
//     inserted immediately after their message
//   - tool_results with no matching tool_use are dropped; a message left
//     empty by the drop is dropped entirely
//
// A history with no pairing violations is returned unchanged, without
// copying. The repair is never written back into the Manager — history
// records what actually happened, the fix only makes one request legal.
func EnsureToolPairing(messages []Message) []Message {
	resolved := make(map[string]struct{})
	issued := make(map[string]struct{})
	for _, m := range messages {
		for _, tr := range m.ToolResults {
			resolved[tr.ToolUseID] = struct{}{}
		}
		for _, tu := range m.ToolUses {
			issued[tu.ID] = struct{}{}
		}
	}

	// Fast path: no dangling calls, no orphan results — nothing to repair,
	// so the input slice is handed through untouched.
	for _, m := range messages {
		for _, tr := range m.ToolResults {
			if _, ok := issued[tr.ToolUseID]; !ok {
				return repair(messages, issued, resolved)
			}
		}
		for _, tu := range m.ToolUses {
			if _, ok := resolved[tu.ID]; !ok {
				return repair(messages, issued, resolved)
			}
		}
	}
	return messages
}

// repair rebuilds messages with the dangling-call and orphan-result fixes
// applied. Precondition: at least one violation exists.
func repair(messages []Message, issued, resolved map[string]struct{}) []Message {
	out := make([]Message, 0, len(messages))
	for _, m := range messages {
		// Orphan tool results: the matching call is not in the history, and
		// keeping them would get the request rejected.
		if len(m.ToolResults) > 0 {
			kept := make([]ToolResultBlock, 0, len(m.ToolResults))
			for _, tr := range m.ToolResults {
				if _, ok := issued[tr.ToolUseID]; ok {
					kept = append(kept, tr)
				}
			}
			if len(kept) == 0 && m.Content == "" && len(m.ToolUses) == 0 {
				continue // empty shell: drop it so role alternation survives
			}
			m.ToolResults = kept
		}

		out = append(out, m)

		// Dangling tool calls: synthesize an error result right after, keeping
		// each tool_use adjacent to its tool_result.
		var missing []ToolResultBlock
		for _, tu := range m.ToolUses {
			if _, ok := resolved[tu.ID]; ok {
				continue
			}
			missing = append(missing, ToolResultBlock{
				ToolUseID: tu.ID,
				Content:   InterruptedToolResult,
				IsError:   true,
			})
			resolved[tu.ID] = struct{}{}
		}
		if len(missing) > 0 {
			out = append(out, Message{Role: RoleUser, ToolResults: missing})
		}
	}
	return out
}
