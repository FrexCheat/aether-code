package permissions

import (
	"regexp"
	"strings"
)

var compoundSplitter = regexp.MustCompile(`&&|\|\||;|\|`)

type pattern struct {
	re     *regexp.Regexp
	reason string
}

func SplitCompound(command string) []string {
	parts := compoundSplitter.Split(command, -1)
	subs := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			subs = append(subs, s)
		}
	}
	if len(subs) == 0 {
		if s := strings.TrimSpace(command); s != "" {
			return []string{s}
		}
	}
	return subs
}

var dangerousPatterns = []pattern{
	// rm -rf with the root as its target, in any flag spelling/order.
	{
		regexp.MustCompile(`(?i)\brm\s+(?:--?[a-z-]+\s+)*(?:-[a-z]*(?:rf|fr)[a-z]*|-[a-z]*r[a-z]*\s+(?:--?[a-z-]+\s+)*-[a-z]*f[a-z]*|-[a-z]*f[a-z]*\s+(?:--?[a-z-]+\s+)*-[a-z]*r[a-z]*)\s+(?:--?[a-z-]+\s+)*(?:["']?)/(?:\*)?["']?(?:\s|$|;|&|\|)`),
		"recursive force delete of the filesystem root (rm -rf /)",
	},
	{
		regexp.MustCompile(`(?i)\brm\b[^;&|]*(?:--recursive[^;&|]*--force|--force[^;&|]*--recursive)[^;&|]*\s(?:["']?)/(?:\*)?["']?(?:\s|$|;|&|\|)`),
		"recursive force delete of the filesystem root (rm -rf /)",
	},
	{
		regexp.MustCompile(`(?i)\bmkfs(?:\.[a-z0-9]+)?\b`),
		"disk formatting (mkfs)",
	},
	{
		regexp.MustCompile(`(?i)\bdd\b[^;&|]*\bof=/dev/`),
		"raw write to a block device (dd of=/dev/…)",
	},
	{
		regexp.MustCompile(`(?i)>\s*/dev/(?:sd|hd|vd|nvme|disk|mmcblk|loop|sr)\w*`),
		"overwrite of a block device",
	},
	{
		regexp.MustCompile(`:\s*\(\s*\)\s*\{.*\}\s*;?\s*:`),
		"fork bomb",
	},
	{
		regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^;|]*\|\s*(?:(?:sudo|env|nohup|command)\s+)*(?:[\w./-]*/)?(?:ba|z|k|da|a|po)?sh\b`),
		"piping a downloaded script into a shell (curl … | sh)",
	},
	{
		regexp.MustCompile(`(?i)\bchmod\b[^;&|]*-[a-z]*R[a-z]*[^;&|]*\s["']?/(?:\*)?["']?(?:\s|$|;|&|\|)`),
		"recursive permission change on the filesystem root (chmod -R /)",
	},
	// Git operations that destroy work. --force matches only when it stands
	// alone, so the safer --force-with-lease is left alone; -D stays
	// case-sensitive so the safe `git branch -d` is not caught.
	{
		regexp.MustCompile(`(?i:git)\s+(?i:push)\b[^;&|]*(?:\s(?i:--force)(?:\s|$)|\s(?i:-f)(?:\s|$))`),
		"force push (overwrites remote history)",
	},
	{
		regexp.MustCompile(`(?i:git)\s+(?i:reset)\b[^;&|]*(?i:--hard)\b`),
		"git reset --hard (discards uncommitted work)",
	},
	{
		regexp.MustCompile(`(?i:git)\s+(?i:clean)\b[^;&|]*\s-[a-z]*f[a-z]*(?:\s|$)`),
		"git clean -f (deletes untracked files)",
	},
	{
		regexp.MustCompile(`(?i:git)\s+(?i:checkout)\b[^;&|]*\s\.\.?(?:\s|$)`),
		"git checkout . (discards uncommitted changes)",
	},
	{
		regexp.MustCompile(`(?i:git)\s+(?i:branch)\b[^;&|]*\s-[a-z]*D[a-z]*(?:\s|$)`),
		"git branch -D (force-deletes a branch)",
	},
}

var protectedPatterns = []pattern{
	// Full path with the .aethercode directory in it.
	{
		regexp.MustCompile(`(?i)\.aethercode[/\\][^\s"']*permissions[^\s"']*\.ya?ml`),
		"the permission rule files are protected",
	},
	// Bare file name, for the `cd .aethercode && … permissions.yaml` spelling.
	{
		regexp.MustCompile(`(?i)(?:^|[\s"'=>/])permissions(?:\.local)?\.ya?ml`),
		"the permission rule files are protected",
	},
}

func DetectDangerous(command string) (bool, string) {
	for _, p := range dangerousPatterns {
		if p.re.MatchString(command) {
			return true, p.reason
		}
	}
	return false, ""
}

func DetectProtectedPath(text string) (bool, string) {
	for _, p := range protectedPatterns {
		// The bare-name pattern matches its leading separator too; the reason
		// reads better without it.
		if m := strings.TrimSpace(p.re.FindString(text)); m != "" {
			return true, p.reason + ": " + m
		}
	}
	return false, ""
}
