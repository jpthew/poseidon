//go:build ios

package jxa

type JxaRunDarwin struct {
	Successful bool
	Results    string
}

func (j *JxaRunDarwin) Success() bool {
	return j.Successful
}

func (j *JxaRunDarwin) Result() string {
	return j.Results
}

func runCommand(encpayload string) (JxaRunDarwin, error) {
	return JxaRunDarwin{Successful: false, Results: "Not implemented on iOS"}, nil
}
