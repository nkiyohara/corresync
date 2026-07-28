package updatecheck

import "testing"

func TestUpgradeAdviceCoversEveryInstallationMethod(t *testing.T) {
	tests := []struct {
		method InstallMethod
		want   string
	}{
		{InstallHomebrew, "brew upgrade nkiyohara/corresync/corresync"},
		{InstallWinGet, "winget upgrade --id nkiyohara.Corresync --exact"},
		{InstallScoop, "scoop update corresync"},
		{InstallDeb, "sudo apt install ./corresync_0.4.0-*_*.deb"},
		{InstallRPM, "sudo dnf install ./corresync-0.4.0-*.rpm"},
		{InstallAPK, "sudo apk add ./corresync_0.4.0-r*_*.apk"},
		{InstallDirect, "corr update"},
	}
	for _, test := range tests {
		if got := UpgradeAdvice(test.method, "v0.4.0"); !contains(got, test.want) {
			t.Errorf("UpgradeAdvice(%q) = %q, want %q", test.method, got, test.want)
		}
	}
}

func TestDetectInstallationRecognizesCatalogPaths(t *testing.T) {
	tests := []struct {
		path string
		want InstallMethod
	}{
		{"/opt/homebrew/Cellar/corresync/0.8.0/bin/corr", InstallHomebrew},
		{"/opt/homebrew/Cellar/corresync/0.7.0/bin/corresync", InstallHomebrew},
		{`C:\Users\reader\scoop\apps\corresync\current\corr.exe`, InstallScoop},
		{`C:\Users\reader\scoop\apps\corresync\current\corresync.exe`, InstallScoop},
		{`C:\Users\reader\AppData\Local\Microsoft\WinGet\Packages\nkiyohara.Corresync_Microsoft.Winget.Source_8wekyb3d8bbwe\corr.exe`, InstallWinGet},
		{`C:\Users\reader\AppData\Local\Microsoft\WinGet\Packages\nkiyohara.Corresync_Microsoft.Winget.Source_8wekyb3d8bbwe\corresync.exe`, InstallWinGet},
		{"/tmp/corresync", InstallDirect},
		// These exact historical paths remain recognized only so an existing
		// v0.6 installation can hand control to the renamed package.
		{"/opt/homebrew/Cellar/owa-bridge/0.4.0/bin/owa", InstallHomebrew},
		{`C:\Users\reader\scoop\apps\owa-bridge\current\owa.exe`, InstallScoop},
		{`C:\Users\reader\AppData\Local\Microsoft\WinGet\Packages\nkiyohara.OWABridge_Microsoft.Winget.Source_8wekyb3d8bbwe\owa.exe`, InstallWinGet},
		{"/tmp/owa", InstallDirect},
	}
	for _, test := range tests {
		if got := DetectInstallation(test.path); got != test.want {
			t.Errorf("DetectInstallation(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func contains(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
