package agentfunctions

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	agentstructs "github.com/MythicMeta/MythicContainer/agent_structs"
	"github.com/MythicMeta/MythicContainer/mythicrpc"
	"github.com/pelletier/go-toml/v2"
)

// Custom version: adds iOS support + webrtc profile to upstream MythicAgents/poseidon 2.3.1.
const version = "2.3.1-custom"

type sleepInfoStruct struct {
	Interval int       `json:"interval"`
	Jitter   int       `json:"jitter"`
	KillDate time.Time `json:"killdate"`
}

var payloadDefinition = agentstructs.PayloadType{
	Name:                                   "poseidon",
	SemVer:                                 version,
	FileExtension:                          "bin",
	Author:                                 "@xorrior, @djhohnstein, @Ne0nd0g, @its_a_feature_",
	SupportedOS:                            []string{agentstructs.SUPPORTED_OS_LINUX, agentstructs.SUPPORTED_OS_MACOS, "ios"},
	Wrapper:                                false,
	CanBeWrappedByTheFollowingPayloadTypes: []string{},
	SupportsDynamicLoading:                 true,
	Description:                            "A fully featured macOS, Linux, and iOS Golang agent.\nNeeds Mythic 3.3.0+",
	SupportedC2Profiles:                    []string{"http", "websocket", "tcp", "dynamichttp", "webshell", "httpx", "dns", "webrtc"},
	MythicEncryptsData:                     true,
	BuildParameters: []agentstructs.BuildParameter{
		{
			Name:          "mode",
			Description:   "Choose build mode. c-shared yields a .dylib/.so/.dll; c-archive yields an archive.",
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
			Description:   "Agent architecture.",
			Required:      false,
			DefaultValue:  "AMD_x64",
			Choices:       []string{"AMD_x64", "ARM_x64"},
			ParameterType: agentstructs.BUILD_PARAMETER_TYPE_CHOOSE_ONE,
			UiPosition:    3,
		},
		{
			Name:          "garble",
			Description:   "Use Garble to obfuscate the output Go executable (slow).",
			Required:      false,
			DefaultValue:  false,
			ParameterType: agentstructs.BUILD_PARAMETER_TYPE_BOOLEAN,
			UiPosition:    4,
		},
		{
			Name:          "debug",
			Description:   "Create a debug build with print statements for debugging.",
			Required:      false,
			DefaultValue:  false,
			ParameterType: agentstructs.BUILD_PARAMETER_TYPE_BOOLEAN,
			UiPosition:    5,
		},
		{
			Name:          "static",
			Description:   "Statically compile the payload (Linux only).",
			Required:      false,
			ParameterType: agentstructs.BUILD_PARAMETER_TYPE_BOOLEAN,
			DefaultValue:  false,
			SupportedOS:   []string{agentstructs.SUPPORTED_OS_LINUX},
			UiPosition:    6,
		},
		{
			Name:          "proxy_bypass",
			Description:   "Ignore HTTP proxy environment settings on the target host.",
			Required:      false,
			DefaultValue:  false,
			ParameterType: agentstructs.BUILD_PARAMETER_TYPE_BOOLEAN,
			GroupName:     "egress",
			UiPosition:    7,
		},
		{
			Name:          "egress_order",
			Description:   "Priority order of egress profiles when multiple are selected.",
			Required:      false,
			ParameterType: agentstructs.BUILD_PARAMETER_TYPE_ARRAY,
			DefaultValue:  []string{"http", "websocket", "dynamichttp", "httpx", "webrtc"},
			GroupName:     "egress",
			UiPosition:    8,
		},
		{
			Name:          "egress_failover",
			Description:   "How egress mechanisms rotate on failure.",
			Required:      false,
			ParameterType: agentstructs.BUILD_PARAMETER_TYPE_CHOOSE_ONE,
			Choices:       []string{"failover"},
			DefaultValue:  "failover",
			GroupName:     "egress",
			UiPosition:    9,
		},
		{
			Name:          "failover_threshold",
			Description:   "Failed attempts before rotating egress.",
			Required:      false,
			ParameterType: agentstructs.BUILD_PARAMETER_TYPE_NUMBER,
			DefaultValue:  10,
			GroupName:     "egress",
			UiPosition:    10,
		},
	},
	SupportsMultipleC2InBuild: true,
	C2ParameterDeviations: map[string]map[string]agentstructs.C2ParameterDeviation{
		"http": {
			"get_uri":         {Supported: false},
			"query_path_name": {Supported: false},
		},
	},
	BuildSteps: []agentstructs.BuildStep{
		{Name: "Configuring", Description: "Generating build command and ldflags"},
		{Name: "Garble", Description: "Applying garble obfuscation (if enabled)"},
		{Name: "Compiling", Description: "Compiling the Golang agent"},
	},
	CheckIfCallbacksAliveFunction: func(message agentstructs.PTCheckIfCallbacksAliveMessage) agentstructs.PTCheckIfCallbacksAliveMessageResponse {
		response := agentstructs.PTCheckIfCallbacksAliveMessageResponse{Success: true, Callbacks: make([]agentstructs.PTCallbacksToCheckResponse, 0)}
		for _, callback := range message.Callbacks {
			if callback.SleepInfo == "" {
				continue
			}
			sleepInfo := map[string]sleepInfoStruct{}
			if err := json.Unmarshal([]byte(callback.SleepInfo), &sleepInfo); err != nil {
				continue
			}
			alive := false
			for activeC2 := range sleepInfo {
				if activeC2 == "websocket" && callback.LastCheckin.Unix() == 0 {
					alive = true
					continue
				}
				if activeC2 == "tcp" || activeC2 == "webrtc" {
					alive = true
					continue
				}
				maxAdd := sleepInfo[activeC2].Interval
				if sleepInfo[activeC2].Jitter > 0 {
					maxAdd += (sleepInfo[activeC2].Jitter / 100) * sleepInfo[activeC2].Interval
				}
				maxAdd *= 2
				latest := callback.LastCheckin.Add(time.Duration(maxAdd) * time.Second)
				if time.Now().UTC().Before(latest) {
					alive = true
					break
				}
			}
			response.Callbacks = append(response.Callbacks, agentstructs.PTCallbacksToCheckResponse{ID: callback.ID, Alive: alive})
		}
		return response
	},
}

func build(payloadBuildMsg agentstructs.PayloadBuildMessage) agentstructs.PayloadBuildResponse {
	resp := agentstructs.PayloadBuildResponse{
		PayloadUUID:        payloadBuildMsg.PayloadUUID,
		Success:            true,
		UpdatedCommandList: &payloadBuildMsg.CommandList,
	}
	if len(payloadBuildMsg.C2Profiles) == 0 {
		resp.Success = false
		resp.BuildStdErr = "Failed to build - must select at least one C2 Profile"
		return resp
	}

	targetOs := "linux"
	switch payloadBuildMsg.SelectedOS {
	case "macOS":
		targetOs = "darwin"
	case "Windows":
		targetOs = "windows"
	case "ios":
		targetOs = "ios"
	}
	macOSVersion := "10.12"

	// --- read build parameters -----------------------------------------------
	egressOrder, err := payloadBuildMsg.BuildParameters.GetArrayArg("egress_order")
	if err != nil {
		resp.Success = false
		resp.BuildStdErr = err.Error()
		return resp
	}
	egressFailover, err := payloadBuildMsg.BuildParameters.GetChooseOneArg("egress_failover")
	if err != nil {
		resp.Success = false
		resp.BuildStdErr = err.Error()
		return resp
	}
	debug, err := payloadBuildMsg.BuildParameters.GetBooleanArg("debug")
	if err != nil {
		resp.Success = false
		resp.BuildStdErr = err.Error()
		return resp
	}
	static, err := payloadBuildMsg.BuildParameters.GetBooleanArg("static")
	if err != nil {
		resp.Success = false
		resp.BuildStdErr = err.Error()
		return resp
	}
	if static && targetOs != "linux" {
		resp.Success = false
		resp.BuildStdErr = "static build is only supported on Linux"
		return resp
	}
	failoverThreshold, err := payloadBuildMsg.BuildParameters.GetNumberArg("failover_threshold")
	if err != nil {
		resp.Success = false
		resp.BuildStdErr = err.Error()
		return resp
	}
	proxyBypass, err := payloadBuildMsg.BuildParameters.GetBooleanArg("proxy_bypass")
	if err != nil {
		resp.Success = false
		resp.BuildStdErr = err.Error()
		return resp
	}
	architecture, err := payloadBuildMsg.BuildParameters.GetStringArg("architecture")
	if err != nil {
		resp.Success = false
		resp.BuildStdErr = err.Error()
		return resp
	}
	mode, err := payloadBuildMsg.BuildParameters.GetStringArg("mode")
	if err != nil {
		resp.Success = false
		resp.BuildStdErr = err.Error()
		return resp
	}
	iosTarget, _ := payloadBuildMsg.BuildParameters.GetStringArg("ios_target")
	garble, err := payloadBuildMsg.BuildParameters.GetBooleanArg("garble")
	if err != nil {
		resp.Success = false
		resp.BuildStdErr = err.Error()
		return resp
	}

	// --- build ldflags -------------------------------------------------------
	repoProfile := "github.com/MythicAgents/poseidon/Payload_Type/poseidon/agent_code/pkg/profiles"
	repoUtils := "github.com/MythicAgents/poseidon/Payload_Type/poseidon/agent_code/pkg/utils"

	ldflagParts := []string{"-s", "-w"}
	if static {
		ldflagParts = append(ldflagParts, "-extldflags=-static")
	}
	ldflagParts = append(ldflagParts,
		"-X", fmt.Sprintf("'%s.UUID=%s'", repoProfile, payloadBuildMsg.PayloadUUID),
		"-X", fmt.Sprintf("'%s.debugString=%v'", repoUtils, debug),
		"-X", fmt.Sprintf("'%s.egress_failover=%s'", repoProfile, egressFailover),
		"-X", fmt.Sprintf("'%s.failedConnectionCountThresholdString=%v'", repoProfile, failoverThreshold),
		"-X", fmt.Sprintf("'%s.proxy_bypass=%v'", repoProfile, proxyBypass),
	)

	egressBytes, err := json.Marshal(egressOrder)
	if err != nil {
		resp.Success = false
		resp.BuildStdErr = err.Error()
		return resp
	}
	ldflagParts = append(ldflagParts, "-X",
		fmt.Sprintf("'%s.egress_order=%s'", repoProfile, base64.StdEncoding.EncodeToString(egressBytes)))

	// --- per-C2-profile initial_config ---------------------------------------
	numericKeys := []string{"callback_jitter", "callback_interval", "callback_port", "port", "failover_threshold", "max_query_length", "max_subdomain_length"}
	boolKeys := []string{"encrypted_exchange_check", "localhost_only"}
	arrayKeys := []string{"callback_domains", "domains"}

	for i := range payloadBuildMsg.C2Profiles {
		profile := &payloadBuildMsg.C2Profiles[i]
		initialConfig := make(map[string]interface{})
		for _, key := range profile.GetArgNames() {
			switch {
			case key == "AESPSK":
				crypto, err := profile.GetCryptoArg(key)
				if err != nil {
					resp.Success = false
					resp.BuildStdErr = "Key error: " + key + "\n" + err.Error()
					return resp
				}
				initialConfig[key] = crypto.EncKey
			case key == "headers":
				hdrs, err := profile.GetDictionaryArg(key)
				if err != nil {
					resp.Success = false
					resp.BuildStdErr = "Key error: " + key + "\n" + err.Error()
					return resp
				}
				initialConfig[key] = hdrs
			case key == "raw_c2_config":
				agentFileID, err := profile.GetStringArg(key)
				if err != nil {
					resp.Success = false
					resp.BuildStdErr = "Key error: " + key + "\n" + err.Error()
					return resp
				}
				cd, err := mythicrpc.SendMythicRPCFileGetContent(mythicrpc.MythicRPCFileGetContentMessage{AgentFileID: agentFileID})
				if err != nil {
					resp.Success = false
					resp.BuildStdErr = "Key error: " + key + "\n" + err.Error()
					return resp
				}
				if !cd.Success {
					resp.Success = false
					resp.BuildStdErr = "Key error: " + key + "\n" + cd.Error
					return resp
				}
				parsed := make(map[string]interface{})
				if jErr := json.Unmarshal(cd.Content, &parsed); jErr != nil {
					if tErr := toml.Unmarshal(cd.Content, &parsed); tErr != nil {
						resp.Success = false
						resp.BuildStdErr = "Key error: " + key + "\n" + tErr.Error()
						return resp
					}
				}
				initialConfig[key] = parsed
			case slices.Contains(numericKeys, key):
				val, err := profile.GetNumberArg(key)
				if err != nil {
					strVal, sErr := profile.GetStringArg(key)
					if sErr != nil {
						resp.Success = false
						resp.BuildStdErr = "Key error: " + key + "\n" + sErr.Error()
						return resp
					}
					n, cErr := strconv.Atoi(strVal)
					if cErr != nil {
						resp.Success = false
						resp.BuildStdErr = "Key error: " + key + "\n" + cErr.Error()
						return resp
					}
					initialConfig[key] = n
				} else {
					initialConfig[key] = int(val)
				}
			case slices.Contains(boolKeys, key):
				val, err := profile.GetBooleanArg(key)
				if err != nil {
					strVal, sErr := profile.GetStringArg(key)
					if sErr != nil {
						resp.Success = false
						resp.BuildStdErr = "Key error: " + key + "\n" + sErr.Error()
						return resp
					}
					initialConfig[key] = strVal == "T"
				} else {
					initialConfig[key] = val
				}
			case slices.Contains(arrayKeys, key):
				arr, err := profile.GetArrayArg(key)
				if err != nil {
					resp.Success = false
					resp.BuildStdErr = "Key error: " + key + "\n" + err.Error()
					return resp
				}
				initialConfig[key] = arr
			default:
				strVal, err := profile.GetStringArg(key)
				if err != nil {
					resp.Success = false
					resp.BuildStdErr = "Key error: " + key + "\n" + err.Error()
					return resp
				}
				if key == "proxy_port" {
					if strVal == "" {
						initialConfig[key] = 0
					} else {
						n, cErr := strconv.Atoi(strVal)
						if cErr != nil {
							resp.Success = false
							resp.BuildStdErr = "Key error: " + key + "\n" + cErr.Error()
							return resp
						}
						initialConfig[key] = n
					}
				} else {
					initialConfig[key] = strVal
				}
			}
		}
		cfgBytes, err := json.Marshal(initialConfig)
		if err != nil {
			resp.Success = false
			resp.BuildStdErr = err.Error()
			return resp
		}
		cfgBase64 := base64.StdEncoding.EncodeToString(cfgBytes)
		resp.BuildStdOut += fmt.Sprintf("%s's config:\n%s\n", profile.Name, string(cfgBytes))
		ldflagParts = append(ldflagParts, "-X",
			fmt.Sprintf("'%s.%s_initial_config=%s'", repoProfile, profile.Name, cfgBase64))
	}
	ldflagParts = append(ldflagParts, "-buildid=")

	// --- compose -tags -------------------------------------------------------
	tags := []string{}
	if static {
		tags = append(tags, "osusergo", "netgo")
	}
	for i := range payloadBuildMsg.C2Profiles {
		tags = append(tags, payloadBuildMsg.C2Profiles[i].Name)
	}
	effectiveMode := mode
	if targetOs == "ios" && mode == "c-shared" {
		// iOS does not support buildmode=c-shared for Go 1.21+; use c-archive then manually link.
		effectiveMode = "c-archive"
	}
	if effectiveMode == "c-shared" {
		tags = append(tags, "shared")
	}
	tags = append(tags, payloadBuildMsg.CommandList...)

	// --- compose go build command -------------------------------------------
	goarch := "amd64"
	if architecture == "ARM_x64" {
		goarch = "arm64"
	}

	cmdEnv := []string{
		"CGO_ENABLED=1",
		fmt.Sprintf("GOOS=%s", targetOs),
		fmt.Sprintf("GOARCH=%s", goarch),
		"GOGARBLE=*",
	}
	switch targetOs {
	case "darwin":
		cmdEnv = append(cmdEnv, "CC=o64-clang", "CXX=o64-clang++")
	case "windows":
		cmdEnv = append(cmdEnv, "CC=x86_64-w64-mingw32-gcc")
	case "ios":
		if iosTarget == "Simulator" {
			cmdEnv = append(cmdEnv, "CC=aarch64-apple-ios16.5-simulator-clang")
		} else {
			cmdEnv = append(cmdEnv, "CC=aarch64-apple-ios16.5-clang")
		}
	default: // linux
		if goarch == "arm64" {
			cmdEnv = append(cmdEnv, "CC=aarch64-linux-gnu-gcc")
		} else {
			cmdEnv = append(cmdEnv, "CC=x86_64-linux-gnu-gcc")
		}
	}

	// output filename
	payloadName := fmt.Sprintf("%s-%s", payloadBuildMsg.PayloadUUID, targetOs)
	if targetOs == "darwin" {
		payloadName += "-" + macOSVersion
	}
	payloadName += "-" + goarch

	archiveName := payloadName + ".a"
	finalName := payloadName
	switch effectiveMode {
	case "c-shared":
		switch targetOs {
		case "windows":
			finalName += ".dll"
		case "darwin":
			finalName += ".dylib"
		default:
			finalName += ".so"
		}
	case "c-archive":
		finalName += ".a"
	}
	// For iOS c-shared we build an archive first then wrap into a .dylib.
	buildOutputName := finalName
	if targetOs == "ios" && mode == "c-shared" {
		buildOutputName = archiveName
	}

	// build exec args
	ldflagStr := strings.Join(ldflagParts, " ")
	execArgs := []string{"go", "build"}
	if garble {
		execArgs = []string{"garble", "-tiny", "-literals", "-debug", "-seed", "random", "build"}
	}
	execArgs = append(execArgs,
		"-tags", strings.Join(tags, ","),
	)
	if effectiveMode != "default" {
		execArgs = append(execArgs, "-buildmode", effectiveMode)
	}
	execArgs = append(execArgs,
		"-ldflags", ldflagStr,
		"-o", "/build/"+buildOutputName,
		".",
	)

	mythicrpc.SendMythicRPCPayloadUpdateBuildStep(mythicrpc.MythicRPCPayloadUpdateBuildStepMessage{
		PayloadUUID: payloadBuildMsg.PayloadUUID,
		StepName:    "Configuring",
		StepSuccess: true,
		StepStdout:  fmt.Sprintf("Env: %v\nCmd: %v", cmdEnv, execArgs),
	})
	if garble {
		mythicrpc.SendMythicRPCPayloadUpdateBuildStep(mythicrpc.MythicRPCPayloadUpdateBuildStepMessage{
			PayloadUUID: payloadBuildMsg.PayloadUUID, StepName: "Garble", StepSuccess: true, StepStdout: "Garble enabled",
		})
	} else {
		mythicrpc.SendMythicRPCPayloadUpdateBuildStep(mythicrpc.MythicRPCPayloadUpdateBuildStepMessage{
			PayloadUUID: payloadBuildMsg.PayloadUUID, StepName: "Garble", StepSkip: true, StepStdout: "Skipped Garble",
		})
	}

	cmd := exec.Command(execArgs[0], execArgs[1:]...)
	cmd.Env = append(os.Environ(), cmdEnv...)
	cmd.Dir = "./poseidon/agent_code/"
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		resp.Success = false
		resp.BuildMessage = "Compilation failed"
		resp.BuildStdErr += stderr.String() + "\n" + err.Error()
		resp.BuildStdOut += stdout.String()
		mythicrpc.SendMythicRPCPayloadUpdateBuildStep(mythicrpc.MythicRPCPayloadUpdateBuildStepMessage{
			PayloadUUID: payloadBuildMsg.PayloadUUID,
			StepName:    "Compiling",
			StepSuccess: false,
			StepStdout:  fmt.Sprintf("failed to compile\n%s\n%s\n%s", stderr.String(), stdout.String(), err.Error()),
		})
		return resp
	}
	resp.BuildStdOut += stdout.String()
	if !garble {
		resp.BuildStdErr += stderr.String()
	}

	// iOS c-shared: wrap archive into a dylib.
	if targetOs == "ios" && mode == "c-shared" {
		cc := "aarch64-apple-ios16.5-clang"
		if iosTarget == "Simulator" {
			cc = "aarch64-apple-ios16.5-simulator-clang"
		}
		finalName = payloadName + ".dylib"
		wrap := exec.Command("/bin/bash", "-c",
			fmt.Sprintf("llvm-ranlib /build/%s && %s -fuse-ld=lld -shared -Wl,-all_load -framework CoreFoundation -framework Foundation -framework Security -o /build/%s /build/%s",
				archiveName, cc, finalName, archiveName))
		var wStdout, wStderr bytes.Buffer
		wrap.Stdout = &wStdout
		wrap.Stderr = &wStderr
		if err := wrap.Run(); err != nil {
			resp.Success = false
			resp.BuildMessage = "iOS dylib link failed"
			resp.BuildStdErr += wStderr.String() + "\n" + err.Error()
			resp.BuildStdOut += wStdout.String()
			return resp
		}
	}

	payloadBytes, err := os.ReadFile(fmt.Sprintf("/build/%s", finalName))
	if err != nil {
		resp.Success = false
		resp.BuildMessage = "Failed to read final payload: " + err.Error()
		return resp
	}
	mythicrpc.SendMythicRPCPayloadUpdateBuildStep(mythicrpc.MythicRPCPayloadUpdateBuildStepMessage{
		PayloadUUID: payloadBuildMsg.PayloadUUID,
		StepName:    "Compiling",
		StepSuccess: true,
		StepStdout:  "Successfully compiled " + finalName,
	})
	resp.Payload = &payloadBytes
	resp.Success = true
	resp.BuildMessage = "Successfully built payload!"
	return resp
}

func Initialize() {
	agentstructs.AllPayloadData.Get("poseidon").AddPayloadDefinition(payloadDefinition)
	agentstructs.AllPayloadData.Get("poseidon").AddBuildFunction(build)
	agentstructs.AllPayloadData.Get("poseidon").AddIcon(filepath.Join(".", "poseidon", "agentfunctions", "poseidon.svg"))
}
