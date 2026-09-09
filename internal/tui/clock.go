package tui

import "time"

// timeSince is indirected so tests can freeze the clock.
var timeSince = time.Since
