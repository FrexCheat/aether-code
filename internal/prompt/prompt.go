// Package prompt holds the built-in system prompts used by AetherCode.
package prompt

// System is the built-in system prompt sent with every request.
// It is not configurable in this phase.
const System = "You are AetherCode, a command-line AI coding assistant. Answer concisely and accurately."

// PlanSegment is appended to the system prompt while plan mode is active. It
// directs the model to explore with the read-only tools and produce a plan
// without making any changes; the request's tool list is filtered to the
// read category for the same purpose (agent.requestTools), so the segment
// never names individual tools.
const PlanSegment = `PLAN MODE is active. Your only goal right now is to research the
codebase and produce an implementation plan — do NOT make any changes.

- Use only the read-only tools available to you to explore the relevant
  files and understand the current design.
- Then present a step-by-step plan: numbered steps, the exact files to touch,
  and what each change does. Note risks and open questions.
- Do not claim you applied anything. You cannot write, edit, or run commands
  in this mode; execution happens after the user switches back to normal mode.`
