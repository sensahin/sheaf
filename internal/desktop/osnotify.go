package desktop

import (
	"os"
	"os/exec"
	"runtime"
)

// Notify shows a desktop notification with the system's own tool:
// osascript on macOS, notify-send on Linux and a PowerShell toast on
// Windows. Text is always passed as arguments or environment, never spliced
// into a script. It does nothing if the tool is missing.
func Notify(a Alert) {
	cmd := notifyCommand(runtime.GOOS, a)
	if cmd == nil {
		return
	}
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}

const windowsToast = `
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
$t = [Security.SecurityElement]::Escape($env:SHEAF_ALERT_TITLE)
$b = [Security.SecurityElement]::Escape($env:SHEAF_ALERT_BODY)
$x = New-Object Windows.Data.Xml.Dom.XmlDocument
$x.LoadXml("<toast><visual><binding template='ToastGeneric'><text>$t</text><text>$b</text></binding></visual></toast>")
$app = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe'
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($app).Show([Windows.UI.Notifications.ToastNotification]::new($x))
`

func notifyCommand(goos string, a Alert) *exec.Cmd {
	switch goos {
	case "darwin":
		return exec.Command("osascript",
			"-e", "on run argv",
			"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
			"-e", "end run",
			a.Title, a.Body)
	case "windows":
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", windowsToast)
		cmd.Env = append(os.Environ(), "SHEAF_ALERT_TITLE="+a.Title, "SHEAF_ALERT_BODY="+a.Body)
		hide(cmd)
		return cmd
	default:
		p, err := exec.LookPath("notify-send")
		if err != nil {
			return nil
		}
		return exec.Command(p, "--app-name=Sheaf", "--", a.Title, a.Body)
	}
}
