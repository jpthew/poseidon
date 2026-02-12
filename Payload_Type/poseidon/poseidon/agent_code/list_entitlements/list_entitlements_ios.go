//go:build ios

package list_entitlements

type DarwinListEntitlements struct {
	Successful  bool
	Message string
	CodeSign int
}

func listEntitlements(pid int) (DarwinListEntitlements, error) {
	return DarwinListEntitlements{Successful: false, Message: "Not implemented on iOS"}, nil
}
func listCodeSign(pid int) (DarwinListEntitlements, error) {
	return DarwinListEntitlements{Successful: false, Message: "Not implemented on iOS"}, nil
}
