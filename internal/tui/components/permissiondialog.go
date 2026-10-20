package components

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/FrexCheat/aether-code/internal/permissions"
)

const maxPermissionRules = 6

type permissionOption struct {
	label    string
	response permissions.Response
}

var permissionOptions = []permissionOption{
	{label: "Allow once", response: permissions.AllowOnce},
	{label: "Allow for this session", response: permissions.AllowSession},
	{label: "Always allow", response: permissions.AllowPermanent},
	{label: "Deny", response: permissions.Reject},
}

// permissionOptionsFor drops the two options that would record a rule when the
// call has none to record.
func permissionOptionsFor(prompt *permissions.Prompt) []permissionOption {
	if len(prompt.Rules) == 0 {
		return []permissionOption{
			{label: "Allow once", response: permissions.AllowOnce},
			{label: "Deny", response: permissions.Reject},
		}
	}
	return permissionOptions
}

// PermissionDialog owns the confirmation prompt. It takes over the bar, so the
// input box and the footer step aside while it is up.
type PermissionDialog struct {
	id     string
	prompt *permissions.Prompt
	cursor int
}

func (d PermissionDialog) Active() bool { return d.prompt != nil }

// Modal reports that the dialog takes the whole input region: the composer and
// its footer step aside, and the output region's live tail is suspended.
func (d PermissionDialog) Modal() bool { return true }

func (d *PermissionDialog) Open(id string, prompt *permissions.Prompt) {
	d.id = id
	d.prompt = prompt
	d.cursor = 0
}

func (d *PermissionDialog) Move(delta int) {
	choices := d.options()
	d.cursor = (d.cursor + delta + len(choices)) % len(choices)
}

func (d PermissionDialog) Selected() permissions.Response { return d.options()[d.cursor].response }

func (d *PermissionDialog) Close() (string, *permissions.Prompt) {
	id, prompt := d.id, d.prompt
	d.id, d.prompt = "", nil
	d.cursor = 0
	return id, prompt
}

func (d PermissionDialog) options() []permissionOption {
	if d.prompt == nil {
		return permissionOptions
	}
	return permissionOptionsFor(d.prompt)
}

// View renders the dialog; the footer argument is ignored — a modal owner
// takes the whole input region.
func (d PermissionDialog) View(width int, _ FooterData) string {
	boxWidth := max(width-2, 20)
	inner := max(boxWidth-2, 10)

	parts := []string{stylePermTitle.Render("Permission required"), ""}
	heading := stylePermTool.Render(d.prompt.ToolName)
	if d.prompt.Action != "" {
		heading += "\n" + lipgloss.Wrap(d.prompt.Action, inner, " -/")
	}
	parts = append(parts, heading)

	if len(d.prompt.Rules) > 0 {
		lines := []string{"", Dim.Render("Granting it would record:")}
		for i, rule := range d.prompt.Rules {
			if i == maxPermissionRules {
				lines = append(lines, Dim.Render(fmt.Sprintf("  … +%d more", len(d.prompt.Rules)-i)))
				break
			}
			lines = append(lines, Dim.Render(lipgloss.Wrap("  • "+rule.String(), inner, " -/")))
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}

	parts = append(parts, "\n")

	choices := d.options()
	options := make([]string, 0, len(choices))
	for i, opt := range choices {
		if i == d.cursor {
			options = append(options, stylePermCursor.Render("› ")+stylePermSelected.Render(opt.label))
			continue
		}
		options = append(options, "  "+opt.label)
	}
	parts = append(parts, strings.Join(options, "\n"), "")
	parts = append(parts, Dim.Render("↑/↓ to move · Enter to confirm · Esc to deny"))

	return stylePermBox.Width(boxWidth).Render(strings.Join(parts, "\n"))
}
