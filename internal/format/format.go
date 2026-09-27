// Package format renders the small display units the UI and the command layer
// must agree on. It is a leaf package: it imports nothing else in the program,
// so no layer has to reach into another to format a number.
package format

import (
	"strconv"
	"strings"
)

// Tokens renders a token count: plain below a thousand, then one decimal place
// with a k suffix ("12.3k").
func Tokens(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return strings.TrimSuffix(strconv.FormatFloat(float64(n)/1000, 'f', 1, 64), ".0") + "k"
}
