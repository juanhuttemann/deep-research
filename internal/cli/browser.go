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
	// several, colon-separated, and the first is the one wanted. Windows
	// paths have a colon of their own, so there it is one command.
	b := getenv("BROWSER")
	if goos != "windows" {
		b, _, _ = strings.Cut(b, ":")
	}
	if b = strings.TrimSpace(b); b != "" {
		return browserEntry(b, lookPath, url)
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

// browserEntry is the command a $BROWSER entry names. Taken whole as a
// program's name, "firefox --new-window" and the conventional "firefox %s"
// named no program, and serve opened nothing. An entry that is a program as
// it stands ("C:\Program Files\...\firefox.exe") is run as it is; else
// it is a command line, with %s standing for the URL, which goes last when
// it has no %s.
func browserEntry(entry string, lookPath func(string) (string, error), url string) (string, []string) {
	if _, err := lookPath(entry); err == nil {
		return entry, []string{url}
	}
	fields := strings.Fields(entry)
	args, placed := make([]string, 0, len(fields)), false
	for _, f := range fields[1:] {
		if strings.Contains(f, "%s") {
			f, placed = strings.ReplaceAll(f, "%s", url), true
		}
		args = append(args, strings.ReplaceAll(f, "%%", "%"))
	}
	if !placed {
		args = append(args, url)
	}
	return fields[0], args
}
