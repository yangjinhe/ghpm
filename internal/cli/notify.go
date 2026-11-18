package cli

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// notifyUpdates shows a desktop notification when updates are available. It
// is used by `ghpm check --notify` from cron, a systemd timer or a launchd
// agent.
func notifyUpdates(ids []string) error {
	body := fmt.Sprintf("%d update(s) available: %s", len(ids), strings.Join(ids, ", "))
	if r := []rune(body); len(r) > 200 {
		body = string(r[:200]) + "…"
	}
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("osascript"); err != nil {
			return fmt.Errorf("osascript not found: %w", err)
		}
		script := fmt.Sprintf(`display notification %s with title "ghpm"`, appleScriptQuote(body))
		cmd := exec.Command("osascript", "-e", script)
		cmd.Stdout, cmd.Stderr = nil, nil
		return cmd.Run()
	}
	path, err := exec.LookPath("notify-send")
	if err != nil {
		return fmt.Errorf("notify-send not found (install libnotify-bin)")
	}
	cmd := exec.Command(path, "ghpm", body)
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Run()
}

// appleScriptQuote escapes a string for use inside an AppleScript literal.
// Backslashes and double quotes are the only characters that need escaping;
// UTF-8 text passes through unchanged.
func appleScriptQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
