package cli

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// openBrowser shows url in the default browser and does not wait for it: a
// browser that was already running hands the page over and exits, and one
// that was not stays open as long as the reader keeps it. A variable, so a
// test of serve opens nothing.
var openBrowser = func(url string) (bool, error) {
	name, args := browserCommand(runtime.GOOS, os.Getenv, exec.LookPath, url)
	if name == "" {
		return false, nil
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return false, err
	}
	go func() { _ = cmd.Wait() }()
	return true, nil
}

// browserCommand is the command that opens url on goos, or "" where there is
// no browser to open: a Linux session with no display is usually SSH, where
// xdg-open would start a text browser on the terminal serve is printing to,
// or fail.
func browserCommand(goos string, getenv func(string) string, lookPath func(string) (string, error), url string) (string, []string) {
	// $BROWSER is the reader's own choice, wherever they are; it may list
	// several, colon-separated, and the first is the one wanted.
	if b, _, _ := strings.Cut(getenv("BROWSER"), ":"); strings.TrimSpace(b) != "" {
		return strings.TrimSpace(b), []string{url}
	}
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	}
	// WSL has Windows' browser and seldom a Linux one: wslview (wslu) opens
	// it, and explorer.exe, which every WSL can run, does when wslu is not
	// installed.
	if getenv("WSL_DISTRO_NAME") != "" || getenv("WSL_INTEROP") != "" {
		if _, err := lookPath("wslview"); err == nil {
			return "wslview", []string{url}
		}
		return "explorer.exe", []string{url}
	}
	if goos == "linux" && getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" {
		return "", nil
	}
	return "xdg-open", []string{url}
}
