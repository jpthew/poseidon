package agentfunctions

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	agentstructs "github.com/MythicMeta/MythicContainer/agent_structs"
)

const version = "2.2.23" // Modifying to clarify in the UI that this is the custom version

var payloadDefinition = agentstructs.PayloadType{
	Name:                                   "poseidon",
	SemVer:                                 version,
	FileExtension:                          "bin",
	Author:                                 "@xorrior, @djhohnstein, @Ne0nd0g, @its_a_feature_",
	SupportedOS:                            []string{agentstructs.SUPPORTED_OS_LINUX, agentstructs.SUPPORTED_OS_MACOS, "ios"},
	Wrapper:                                false,
	CanBeWrappedByTheFollowingPayloadTypes: []string{},
	SupportsDynamicLoading:                 false,
	Description:                            fmt.Sprintf("A fully featured macOS, Linux, and iOS Golang agent.\nNeeds Mythic 3.3.0+"),
	SupportedC2Profiles:                    []string{"http", "websocket", "tcp", "dynamichttp", "webshell", "httpx", "dns"},
	MythicEncryptsData:                     true,
	BuildParameters: []agentstructs.BuildParameter{
		{
			Name:          "mode",
			Description:   "Choose build mode. Select c-shared for an iOS .dylib.",
			Required:      false,
			DefaultValue:  "default",
			Choices:       []string{"default", "c-archive", "c-shared"},
			ParameterType: agentstructs.BUILD_PARAMETER_TYPE_CHOOSE_ONE,
			UiPosition:    1,
		},
		{
			Name:          "ios_target",
			Description:   "Target environment for iOS.",
			Required:      false,
			DefaultValue:  "Simulator",
			Choices:       []string{"Simulator", "Hardware"},
			ParameterType: agentstructs.BUILD_PARAMETER_TYPE_CHOOSE_ONE,
			SupportedOS:   []string{"ios"},
			UiPosition:    2,
		},
		{
			Name:          "architecture",
			Description:   "Choose the agent's architecture",
			Required:      false,
			DefaultValue:  "ARM_x64",
			Choices:       []string{"AMD_x64", "ARM_x64"},
			ParameterType: agentstructs.BUILD_PARAMETER_TYPE_CHOOSE_ONE,
			UiPosition:    3,
		},
		{
			Name:          "garble",
			Description:   "Use Garble to obfuscate the output.",
			Required:      false,
			DefaultValue:  false,
			ParameterType: agentstructs.BUILD_PARAMETER_TYPE_BOOLEAN,
			UiPosition:    4,
		},
	},
	BuildSteps: []agentstructs.BuildStep{
		{Name: "Configuring", Description: "Generating build command"},
		{Name: "Compiling", Description: "Compiling Golang agent"},
	},
}

func build(payloadBuildMsg agentstructs.PayloadBuildMessage) agentstructs.PayloadBuildResponse {
	payloadBuildResponse := agentstructs.PayloadBuildResponse{
		PayloadUUID:        payloadBuildMsg.PayloadUUID,
		Success:            true,
		UpdatedCommandList: &payloadBuildMsg.CommandList,
	}

	targetOs := "linux"
	if payloadBuildMsg.SelectedOS == "macOS" {
		targetOs = "darwin"
	} else if payloadBuildMsg.SelectedOS == "ios" {
		targetOs = "ios"
	}

	architecture, _ := payloadBuildMsg.BuildParameters.GetStringArg("architecture")
	mode, _ := payloadBuildMsg.BuildParameters.GetStringArg("mode")
	iosTarget, _ := payloadBuildMsg.BuildParameters.GetStringArg("ios_target")
	garble, _ := payloadBuildMsg.BuildParameters.GetBooleanArg("garble")

	// Satisfy "garble declared and not used" error
	_ = garble 

	goarch := "amd64"
	if architecture == "ARM_x64" {
		goarch = "arm64"
	}

	// Construct build command
	command := fmt.Sprintf("CGO_ENABLED=1 GOOS=%s GOARCH=%s ", targetOs, goarch)

	// Handle Compiler and Flags
	if targetOs == "ios" {
		if iosTarget == "Simulator" {
			command += "CC=aarch64-apple-ios16.5-simulator-clang "
		} else {
			command += "CC=aarch64-apple-ios16.5-clang "
		}
	} else if targetOs == "darwin" {
		command += "CC=o64-clang CXX=o64-clang++ "
	}

	command += "go build "

	// Logic Switch: Force c-archive for iOS shared builds
	effectiveMode := mode
	if targetOs == "ios" && mode == "c-shared" {
		effectiveMode = "c-archive"
	}

	if effectiveMode != "default" {
		command += fmt.Sprintf("-buildmode=%s ", effectiveMode)
	}

	// Temporary archive name and final payload name
	archiveName := fmt.Sprintf("%s.a", payloadBuildMsg.PayloadUUID)
	payloadName := fmt.Sprintf("%s-%s-%s", payloadBuildMsg.PayloadUUID, targetOs, goarch)

	ldflags := fmt.Sprintf("-s -w -X 'github.com/MythicAgents/poseidon/Payload_Type/poseidon/agent_code/pkg/profiles.UUID=%s'", payloadBuildMsg.PayloadUUID)
	if targetOs == "ios" {
		ldflags += " -extldflags \"-fuse-ld=lld\""
	}

	// Step 1: Run the Go build to create the .a (archive)
	if targetOs == "ios" && mode == "c-shared" {
		command += fmt.Sprintf("-ldflags \"%s\" -o /build/%s .", ldflags, archiveName)

		// Step 2: Manual Linking for iOS Shared Library
		payloadName += ".dylib" // The final output we want

		cc := "aarch64-apple-ios16.5-clang"
		if iosTarget == "Simulator" {
			cc = "aarch64-apple-ios16.5-simulator-clang"
		}

		// We use -shared and point to the archive to wrap it into a dylib
		command += fmt.Sprintf(" && llvm-ranlib /build/%s", archiveName)
		command += fmt.Sprintf(" && %s -fuse-ld=lld -shared -Wl,-all_load -framework CoreFoundation -framework Foundation -framework Security -o /build/%s /build/%s", cc, payloadName, archiveName)
	} else {
		if mode == "c-shared" {
			payloadName += ".dylib"
		}
		command += fmt.Sprintf("-ldflags \"%s\" -o /build/%s .", ldflags, payloadName)
	}

	cmd := exec.Command("/bin/bash", "-c", command)
	cmd.Dir = "./poseidon/agent_code/"
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		payloadBuildResponse.Success = false
		payloadBuildResponse.BuildStdErr = stderr.String() + "\n" + stdout.String()
		return payloadBuildResponse
	}

	payloadBytes, err := os.ReadFile(fmt.Sprintf("/build/%s", payloadName))
	if err != nil {
		payloadBuildResponse.Success = false
		payloadBuildResponse.BuildMessage = "Failed to find final payload on disk"
		return payloadBuildResponse
	}

	payloadBuildResponse.Payload = &payloadBytes
	payloadBuildResponse.Success = true
	payloadBuildResponse.BuildMessage = "Successfully built payload!"

	return payloadBuildResponse
}

func Initialize() {
	agentstructs.AllPayloadData.Get("poseidon").AddPayloadDefinition(payloadDefinition)
	agentstructs.AllPayloadData.Get("poseidon").AddBuildFunction(build)
	agentstructs.AllPayloadData.Get("poseidon").AddIcon(filepath.Join(".", "poseidon", "agentfunctions", "poseidon.svg"))
}
