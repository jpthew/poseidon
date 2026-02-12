//go:build ios

package clipboard_monitor

func CheckClipboard(count int) (string, error) {
	return "", nil
}

func GetClipboardCount() (int, error) {
	return 0, nil
}

func GetFrontmostApp() (string, error) {
	return "", nil
}

func WaitForTime() {
}
