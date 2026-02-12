//go:build ios

package execute_library

type DarwinExecuteLibrary struct {
	Message string
}

func executeLibrary(filePath string, functionName string, args []string) (DarwinExecuteLibrary, error) {
	return DarwinExecuteLibrary{Message: "Not implemented on iOS"}, nil
}
