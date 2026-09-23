package components

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/FrexCheat/aether-code/internal/permissions"
)

const maxPermissionRules = 6

var (
	stylePermBox      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("3")).Padding(0, 1)
	stylePermTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	stylePermTool     = lipgloss.NewStyle().Bold(true)
	stylePermCursor   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	stylePermSelected = lipgloss.NewStyle().Bold(true)
)

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
func permissionOptionsFor(req *permissions.Request) []permissionOption {
	if len(req.Rules) == 0 {
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
	req    *permissions.Request
	cursor int
}

func (d PermissionDialog) Active() bool { return d.req != nil }

func (d *PermissionDialog) Open(req *permissions.Request) {
	d.req = req
	d.cursor = 0
}

func (d *PermissionDialog) Move(delta int) {
	choices := d.options()
	d.cursor = (d.cursor + delta + len(choices)) % len(choices)
}

func (d PermissionDialog) Selected() permissions.Response { return d.options()[d.cursor].response }

func (d *PermissionDialog) Close() *permissions.Request {
	req := d.req
	d.req = nil
	d.cursor = 0
	return req
}

func (d PermissionDialog) options() []permissionOption {
	if d.req == nil {
		return permissionOptions
	}
	return permissionOptionsFor(d.req)
}

func (d PermissionDialog) View(width int) string {
	boxWidth := max(width-2, 20)
	inner := max(boxWidth-2, 10)

	parts := []string{stylePermTitle.Render("Permission required"), ""}
	heading := stylePermTool.Render(d.req.ToolName)
	if d.req.Action != "" {
		heading += "\n" + lipgloss.Wrap(d.req.Action, inner, " -/")
	}
	parts = append(parts, heading)

	if len(d.req.Rules) > 0 {
		lines := []string{"", Dim.Render("Granting it would record:")}
		for i, rule := range d.req.Rules {
			if i == maxPermissionRules {
				lines = append(lines, Dim.Render(fmt.Sprintf("  … +%d more", len(d.req.Rules)-i)))
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
