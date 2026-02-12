//go:build ios

package functions

import (
	"fmt"
	"os/user"
)

func isElevated() bool {
	return false
}
func getArchitecture() string {
	return "arm64"
}
func getProcessName() string {
	return "poseidon"
}
func getDomain() string {
	return ""
}
func getOS() string {
	return "iOS"
}
func getUser() string {
    u, err := user.Current()
    if err != nil {
        return ""
    }
    return u.Username
}
func getEffectiveUser() string {
    return getUser()
}
func getPID() int {
	return 0
}
func getHostname() string {
	return "iPhone"
}
func getCwd() string {
	return "/"
}
func getOSVersion() string {
    return "16.5"
}

func UINT32ByteCountDecimal(b uint32) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint32(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float32(b)/float32(div), "kMGTPE"[exp])
}

func UINT64ByteCountDecimal(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "kMGTPE"[exp])
}
