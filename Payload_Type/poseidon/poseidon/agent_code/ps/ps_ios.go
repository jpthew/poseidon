//go:build ios

package ps

type ProcessDetails struct {
	ProcessID          int                    `json:"process_id"`
	ProcessName        string                 `json:"name"`
	BinaryPath         string                 `json:"bin_path"`
	User               int                    `json:"user_id"`
	ParentProcessID    int                    `json:"parent_process_id"`
	Architecture       string                 `json:"architecture"`
	Sandbox            bool                   `json:"sandboxed"`
	ScriptingProps     map[string]interface{} `json:"scripting_properties"`
	Args               []string               `json:"args"`
	Env                map[string]string      `json:"environment"`
}

func (p ProcessDetails) Pid() int { return p.ProcessID }
func (p ProcessDetails) PPid() int { return p.ParentProcessID }
func (p ProcessDetails) Arch() string { return p.Architecture }
func (p ProcessDetails) Owner() string { return "" }
func (p ProcessDetails) BinPath() string { return p.BinaryPath }
func (p ProcessDetails) ProcessArguments() []string { return p.Args }
func (p ProcessDetails) ProcessEnvironment() map[string]string { return p.Env }
func (p ProcessDetails) SandboxPath() string { return "" }
func (p ProcessDetails) ScriptingProperties() map[string]interface{} { return p.ScriptingProps }
func (p ProcessDetails) Name() string { return p.ProcessName }
func (p ProcessDetails) BundleID() string { return "" }
func (p ProcessDetails) AdditionalInfo() map[string]interface{} { return nil }

func Processes() ([]Process, error) {
	return []Process{}, nil
}
