package cli

import (
	"fmt"
	"os/exec"
	"strings"
)

// notifyUpdates shows a desktop notification when updates are available. It
// is used by `ghpm check --notify` from cron or a systemd timer.
func notifyUpdates(ids []string) error {
	path, err := exec.LookPath("notify-send")
	if err != nil {
		return fmt.Errorf("notify-send not found (install libnotify-bin)")
	}
	body := fmt.Sprintf("%d update(s) available: %s", len(ids), strings.Join(ids, ", "))
	if len(body) > 200 {
		body = body[:200] + "…"
	}
	cmd := exec.Command(path, "ghpm", body)
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Run()
}
