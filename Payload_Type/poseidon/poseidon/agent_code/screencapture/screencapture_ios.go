//go:build ios

package screencapture

type DarwinScreenShot struct {}
func (d DarwinScreenShot) Monitor() int { return 0 }
func (d DarwinScreenShot) Data() []byte { return nil }

func getscreenshot() ([]ScreenShot, error) {
	return []ScreenShot{}, nil
}
