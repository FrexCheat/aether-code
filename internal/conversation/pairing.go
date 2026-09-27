package conversation

const (
	InterruptedToolResult = "Tool execution was interrupted. The tool may or may not have completed; verify before relying on its effects."
)

// IndexToolResults maps each tool-use ID to its result, so callers that pair
// calls with results need not each walk the history.
func IndexToolResults(messages []Message) map[string]ToolResultBlock {
	index := make(map[string]ToolResultBlock)
	for _, m := range messages {
		for _, tr := range m.ToolResults {
			index[tr.ToolUseID] = tr
		}
	}
	return index
}

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
