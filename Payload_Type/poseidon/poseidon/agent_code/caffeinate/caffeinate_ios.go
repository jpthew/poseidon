//go:build ios

package caffeinate

type DarwinCaffeinate struct {
    success bool
    result string
}
func (d DarwinCaffeinate) Success() bool { return d.success }
func (d DarwinCaffeinate) Result() string { return d.result }

func runCommand(enable bool) (CaffeinateRun, error) {
    return DarwinCaffeinate{false, "Not implemented on iOS"}, nil
}
