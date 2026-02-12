//go:build ios

package clipboard

func GetClipboard(readTypes []string) (string, error) {
	return "Not implemented on iOS", nil
}
