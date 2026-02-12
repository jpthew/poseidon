//go:build ios

package lsopen

type LsOpenResult struct {
    Successful bool
}

func runCommand(app string, hide bool, args []string) (LsOpenResult, error) {
	return LsOpenResult{Successful: false}, nil
}
