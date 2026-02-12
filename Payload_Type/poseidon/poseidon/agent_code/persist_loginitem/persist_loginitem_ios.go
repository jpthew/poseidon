//go:build ios

package persist_loginitem

type LoginItemResult struct {
    Message string
}

func runCommand(path, name string, global, list, remove bool) LoginItemResult {
	return LoginItemResult{Message: "Not implemented on iOS"}
}
