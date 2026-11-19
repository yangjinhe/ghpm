package main

import (
	"fmt"
	"strings"

	"ghpm/internal/app"
)

func appStatus(v app.AppView) string {
	switch {
	case !v.Installed:
		return "not installed"
	case v.Pinned:
		return "pinned"
	case v.CheckError != "":
		return "check failed"
	case v.UpdateAvailable && v.Comparable:
		return "update available"
	case v.UpdateAvailable:
		return "different version"
	case v.LatestVersion == "":
		return "not checked"
	default:
		return "up to date"
	}
}

func versionsOf(v app.AppView) []string {
	if v.State == nil {
		return nil
	}
	return v.State.Versions
}

func counts(views []app.AppView) (installed, updates int) {
	for _, v := range views {
		if v.Installed {
			installed++
		}
		if v.Installed && v.UpdateAvailable && !v.Pinned {
			updates++
		}
	}
	return installed, updates
}

func humanRate(bytesPerSec float64) string {
	const unit = 1024.0
	switch {
	case bytesPerSec >= unit*unit*unit:
		return fmt.Sprintf("%.1f GiB/s", bytesPerSec/(unit*unit*unit))
	case bytesPerSec >= unit*unit:
		return fmt.Sprintf("%.1f MiB/s", bytesPerSec/(unit*unit))
	case bytesPerSec >= unit:
		return fmt.Sprintf("%.1f KiB/s", bytesPerSec/unit)
	default:
		return fmt.Sprintf("%.0f B/s", bytesPerSec)
	}
}

func installSummary(results []app.InstallResult) string {
	ok := 0
	var warnings int
	for _, r := range results {
		if r.Err == nil {
			ok++
		}
		warnings += len(r.Warnings)
	}
	msg := fmt.Sprintf("%d app(s) updated", ok)
	if warnings > 0 {
		msg += fmt.Sprintf(", %d warning(s)", warnings)
	}
	return msg
}

func installError(results []app.InstallResult) error {
	var failed []string
	for _, r := range results {
		if r.Err != nil {
			failed = append(failed, r.ID+": "+r.Err.Error())
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return fmt.Errorf("%d failed: %s", len(failed), strings.Join(failed, "; "))
}
