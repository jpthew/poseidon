//go:build ios

package libinject

type DarwinInjection struct {
	Target      int
	Successful  bool
	Payload     []byte
	LibraryPath string
}

func (l *DarwinInjection) TargetPid() int {
	return l.Target
}

func (l *DarwinInjection) Success() bool {
	return l.Successful
}

func (l *DarwinInjection) Shellcode() []byte {
	return l.Payload
}

func (l *DarwinInjection) SharedLib() string {
	return l.LibraryPath
}

func injectLibrary(pid int, path string) (DarwinInjection, error) {
	return DarwinInjection{Target: pid, Successful: false}, nil
}
