package main

import (
	"os"
	"path/filepath"
)

func douyinBrowserExecutable() string {
	home, _ := os.UserHomeDir()
	for _, root := range []string{"/Applications", filepath.Join(home, "Applications")} {
		for _, browser := range []struct{ bundle, executable string }{{"Google Chrome", "Google Chrome"}, {"Microsoft Edge", "Microsoft Edge"}, {"Chromium", "Chromium"}} {
			candidate := filepath.Join(root, browser.bundle+".app", "Contents", "MacOS", browser.executable)
			if executableFile(candidate) {
				return candidate
			}
		}
	}
	return ""
}
