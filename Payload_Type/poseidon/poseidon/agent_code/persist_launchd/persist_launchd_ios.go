//go:build ios

package persist_launchd

import (
    "github.com/MythicAgents/poseidon/Payload_Type/poseidon/agent_code/pkg/utils/structs"
)

func runCommand(task structs.Task) {
	msg := task.NewResponse()
	msg.SetError("Not implemented on iOS")
	task.Job.SendResponses <- msg
}
