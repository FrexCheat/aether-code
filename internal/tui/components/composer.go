package components

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Composer is the input region's fallback owner: the text box, the completion
// menu below it and the footer. The box and the menu are one interaction
// component — the menu never hides the box, unlike a modal owner.
type Composer struct {
	input InputBox
	menu  CompletionMenu
}

func NewComposer() Composer { return Composer{input: NewInputBox()} }

func (c Composer) Active() bool { return true }

func (c Composer) Modal() bool { return false }

func (c *Composer) SetWidth(w int)     { c.input.SetWidth(w) }
func (c *Composer) SetMaxHeight(h int) { c.input.SetMaxHeight(h) }

func (c Composer) Value() string      { return c.input.Value() }
func (c *Composer) SetValue(v string) { c.input.SetValue(v) }
func (c Composer) Focus() tea.Cmd     { return c.input.Focus() }

func (c *Composer) Reset() {
	c.input.Reset()
	c.menu.Close()
}

func (c Composer) Update(msg tea.Msg) (Composer, tea.Cmd) {
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(msg)
	return c, cmd
}

func (c *Composer) OpenMenu(options []CompletionOption) { c.menu.Open(options) }
func (c *Composer) CloseMenu()                          { c.menu.Close() }
func (c Composer) MenuActive() bool                     { return c.menu.Active() }
func (c *Composer) MoveMenu(delta int)                  { c.menu.Move(delta) }
func (c Composer) SelectedMenu() (CompletionOption, bool) {
	return c.menu.Selected()
}

// View renders the box, then the completion menu when it is open, then the
// footer.
func (c Composer) View(width int, footer FooterData) string {
	parts := []string{c.input.boxView()}
	if c.menu.Active() {
		parts = append(parts, c.menu.View(width))
	}
	parts = append(parts, c.input.footerLine(footer))
	return strings.Join(parts, "\n")
}
