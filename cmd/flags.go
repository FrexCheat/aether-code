package main

import (
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/FrexCheat/aether-code/internal/session"
)

// flags is the parsed command line: the session resume entry points, nothing
// else. With no flags aethercode starts a fresh session.
type flags struct {
	cont     bool
	resumeID string
	list     bool
}

func parseFlags(args []string, stderr io.Writer) (flags, error) {
	fs := flag.NewFlagSet("aethercode", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		cont     = fs.Bool("continue", false, "resume the most recent session for this directory")
		resumeID = fs.String("resume", "", "resume a session by id")
		list     = fs.Bool("list", false, "list sessions for this directory and exit")
	)
	if err := fs.Parse(args); err != nil {
		return flags{}, err // the flag package already reported it
	}
	out := flags{cont: *cont, resumeID: *resumeID, list: *list}
	switch {
	case out.cont && out.resumeID != "":
		return flags{}, usageError(fs, "--continue and --resume are mutually exclusive")
	case out.list && (out.cont || out.resumeID != ""):
		return flags{}, usageError(fs, "--list cannot be combined with --continue or --resume")
	}
	return out, nil
}

func usageError(fs *flag.FlagSet, msg string) error {
	err := fmt.Errorf("%s", msg)
	fmt.Fprintln(fs.Output(), "aethercode:", err)
	fs.Usage()
	return err
}

// printList renders the --list table: what a session scan derives, with
// unreadable records called out so a damaged archive is visible before it is
// resumed.
func printList(w io.Writer, cwd string, infos []session.Info) {
	if len(infos) == 0 {
		fmt.Fprintln(w, "no sessions for "+cwd)
		return
	}
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tMODIFIED\tMESSAGES\tTITLE")
	for _, info := range infos {
		title := info.Title
		if title == "" {
			title = "(empty)"
		}
		messages := fmt.Sprintf("%d", info.Messages)
		if info.Skipped > 0 {
			messages += fmt.Sprintf(" (%d skipped)", info.Skipped)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", info.ID, relativeTime(info.ModTime), messages, title)
	}
	tw.Flush()
}

func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours())/24)
	}
}
