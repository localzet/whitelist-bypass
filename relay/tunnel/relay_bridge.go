package tunnel

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"whitelist-bypass/relay/common"
	"whitelist-bypass/relay/egress"
)

const verboseUDPLogging = false

type creatorUDP struct {
	session egress.UDPSession
}

func (c *creatorUDP) writePacket(data []byte, dst string) error {
	return c.session.WriteTo(data, dst)
}

func (c *creatorUDP) read(buf []byte) (int, error) {
	return c.session.Read(buf)
}

func (c *creatorUDP) setReadDeadline(t time.Time) error {
	return c.session.SetReadDeadline(t)
}

func (c *creatorUDP) close() error {
	return c.session.Close()
}

type udpClient struct {
	udpConn    *net.UDPConn
	clientAddr *net.UDPAddr
	socksHdr   []byte
	mapKey     string
}

type RelayBridge struct {
	tunnelMu          sync.RWMutex
	tunnel            DataTunnel
	conns             sync.Map
	udpClients        sync.Map
	udpSessions       sync.Map
	nackedConns       sync.Map
	nextID            atomic.Uint32
	logFn             func(string, ...any)
	mode              string
	readBuf           int
	ready             chan struct{}
	once              sync.Once
	socksUser         string
	socksPass         string
	egressMu          sync.RWMutex
	egressReg         *egress.Registry
	dialer            egress.Dialer
	egressID          string
	requestedEgressID string
	handshakeDone     chan struct{}
	handshakeOnce     sync.Once
	handshakeErr      atomic.Value

	persistentListener atomic.Bool
	listenerMu         sync.Mutex
	listener           net.Listener
	closed             atomic.Bool

	onPeerConfigMu sync.Mutex
	onPeerConfig   func(fps, batch, trackCount int)

	onConfigAckMu sync.Mutex
	onConfigAck   func()

	onSessionCreateMu sync.Mutex
	onSessionCreate   func(SessionCreateRequest) (SessionReady, error)

	onSessionReadyMu sync.Mutex
	onSessionReady   func(SessionReady)

	onCookieSubmitMu sync.Mutex
	onCookieSubmit   func(CookieSubmit) (CookieAck, error)

	onCookieAckMu sync.Mutex
	onCookieAck   func(CookieAck)

	onControlErrorMu sync.Mutex
	onControlError   func(ControlError)
}

func (rb *RelayBridge) SetOnPeerConfig(fn func(fps, batch, trackCount int)) {
	rb.onPeerConfigMu.Lock()
	rb.onPeerConfig = fn
	rb.onPeerConfigMu.Unlock()
}

func (rb *RelayBridge) SetOnConfigAck(fn func()) {
	rb.onConfigAckMu.Lock()
	rb.onConfigAck = fn
	rb.onConfigAckMu.Unlock()
}

func (rb *RelayBridge) SetOnSessionCreate(fn func(SessionCreateRequest) (SessionReady, error)) {
	rb.onSessionCreateMu.Lock()
	rb.onSessionCreate = fn
	rb.onSessionCreateMu.Unlock()
}

func (rb *RelayBridge) SetOnSessionReady(fn func(SessionReady)) {
	rb.onSessionReadyMu.Lock()
	rb.onSessionReady = fn
	rb.onSessionReadyMu.Unlock()
}

func (rb *RelayBridge) RequestSession(request SessionCreateRequest) {
	rb.send(ControlConnID, MsgSessionCreate, EncodeSessionCreatePayload(request))
}

func (rb *RelayBridge) SetOnCookieSubmit(fn func(CookieSubmit) (CookieAck, error)) {
	rb.onCookieSubmitMu.Lock()
	rb.onCookieSubmit = fn
	rb.onCookieSubmitMu.Unlock()
}

func (rb *RelayBridge) SetOnCookieAck(fn func(CookieAck)) {
	rb.onCookieAckMu.Lock()
	rb.onCookieAck = fn
	rb.onCookieAckMu.Unlock()
}

func (rb *RelayBridge) SubmitCookies(cookie CookieSubmit) {
	rb.send(ControlConnID, MsgCookieSubmit, EncodeCookieSubmitPayload(cookie))
}

func (rb *RelayBridge) SetOnControlError(fn func(ControlError)) {
	rb.onControlErrorMu.Lock()
	rb.onControlError = fn
	rb.onControlErrorMu.Unlock()
}

func NewRelayBridgeWithAuth(tunnel DataTunnel, mode string, readBuf int, logFn func(string, ...any), socksUser, socksPass string) *RelayBridge {
	rb := NewRelayBridge(tunnel, mode, readBuf, logFn)
	rb.socksUser = socksUser
	rb.socksPass = socksPass
	return rb
}

func NewRelayBridge(tunnel DataTunnel, mode string, readBuf int, logFn func(string, ...any)) *RelayBridge {
	rb := &RelayBridge{
		tunnel:        tunnel,
		logFn:         logFn,
		mode:          mode,
		readBuf:       readBuf,
		ready:         make(chan struct{}),
		egressReg:     egress.DirectRegistry(),
		handshakeDone: make(chan struct{}),
	}
	if mode == "creator" {
		rb.selectCreatorEgress("")
	}
	tunnel.SetOnData(rb.handleTunnelData)
	tunnel.SetOnClose(rb.handleTunnelClose)
	return rb
}

func (rb *RelayBridge) SetPersistentListener(persistent bool) {
	rb.persistentListener.Store(persistent)
}

func (rb *RelayBridge) SetUpstreamSocks(addr, user, pass string) {
	reg, err := egress.LegacySOCKSRegistry(addr, user, pass)
	if err != nil {
		rb.setHandshakeError(err)
		rb.logFn("relay: invalid legacy upstream: %v", err)
		return
	}
	rb.SetEgressRegistry(reg)
}

func (rb *RelayBridge) SetEgressRegistry(reg *egress.Registry) {
	if reg == nil {
		reg = egress.DirectRegistry()
	}
	rb.egressMu.Lock()
	rb.egressReg = reg
	rb.dialer = nil
	rb.egressID = ""
	rb.egressMu.Unlock()
	if rb.mode == "creator" {
		rb.selectCreatorEgress("")
	}
}

func (rb *RelayBridge) SetRequestedEgressID(id string) {
	rb.egressMu.Lock()
	rb.requestedEgressID = strings.TrimSpace(id)
	rb.egressMu.Unlock()
}

func (rb *RelayBridge) SwapTunnel(newTunnel DataTunnel) {
	rb.tunnelMu.Lock()
	rb.tunnel = newTunnel
	rb.tunnelMu.Unlock()
	newTunnel.SetOnData(rb.handleTunnelData)
	newTunnel.SetOnClose(rb.handleTunnelClose)
	rb.closeAll()
	if rb.mode == "joiner" {
		rb.sendClientHello()
	}
}

func (rb *RelayBridge) currentTunnel() DataTunnel {
	rb.tunnelMu.RLock()
	defer rb.tunnelMu.RUnlock()
	return rb.tunnel
}

func (rb *RelayBridge) handleTunnelClose() {
	if rb.persistentListener.Load() {
		rb.closeAll()
		return
	}
	rb.Close()
}

func (rb *RelayBridge) closeAll() {
	var ids []uint32
	rb.conns.Range(func(key, value any) bool {
		if id, ok := key.(uint32); ok {
			ids = append(ids, id)
		}
		switch v := value.(type) {
		case net.Conn:
			v.Close()
		case *socksConn:
			v.conn.Close()
		}
		rb.conns.Delete(key)
		return true
	})
	udpCount := 0
	rb.udpClients.Range(func(key, value any) bool {
		udpCount++
		switch v := value.(type) {
		case *creatorUDP:
			v.close()
		case *udpClient:
			v.udpConn.Close()
		}
		rb.udpClients.Delete(key)
		return true
	})
	rb.udpSessions.Range(func(key, _ any) bool {
		rb.udpSessions.Delete(key)
		return true
	})
	rb.nackedConns.Range(func(key, _ any) bool {
		rb.nackedConns.Delete(key)
		return true
	})
	rb.logFn("relay: closeAll mode=%s tcp=%d udp=%d ids=%v nextID=%d", rb.mode, len(ids), udpCount, ids, rb.nextID.Load())
}

func (rb *RelayBridge) Reset() {
	rb.closeAll()
}

func (rb *RelayBridge) Close() {
	if !rb.closed.CompareAndSwap(false, true) {
		return
	}
	rb.listenerMu.Lock()
	ln := rb.listener
	rb.listener = nil
	rb.listenerMu.Unlock()
	if ln != nil {
		rb.logFn("relay: bridge Close closing socks listener")
		ln.Close()
	}
	rb.closeAll()
}

func (rb *RelayBridge) Stats() (tcpConns, udpConns int, nextID uint32) {
	rb.conns.Range(func(_, _ any) bool { tcpConns++; return true })
	rb.udpClients.Range(func(_, _ any) bool { udpConns++; return true })
	return tcpConns, udpConns, rb.nextID.Load()
}

func (rb *RelayBridge) MarkReady() {
	if rb.mode == "joiner" {
		rb.sendClientHello()
	}
	rb.once.Do(func() { close(rb.ready) })
}

func (rb *RelayBridge) sendClientHello() {
	requested := rb.requestedEgress()
	rb.send(ControlConnID, MsgClientHello, EncodeClientHelloPayload(requested))
	if requested != "" {
		rb.logFn("relay: requested egress %q", requested)
	}
}

func (rb *RelayBridge) requestedEgress() string {
	rb.egressMu.RLock()
	defer rb.egressMu.RUnlock()
	return rb.requestedEgressID
}

func (rb *RelayBridge) currentEgressRegistry() *egress.Registry {
	rb.egressMu.RLock()
	defer rb.egressMu.RUnlock()
	return rb.egressReg
}

func (rb *RelayBridge) selectedDialer() (egress.Dialer, string, error) {
	rb.egressMu.RLock()
	dialer := rb.dialer
	id := rb.egressID
	rb.egressMu.RUnlock()
	if dialer == nil {
		return nil, "", fmt.Errorf("egress is not selected")
	}
	return dialer, id, nil
}

func (rb *RelayBridge) selectCreatorEgress(requestedID string) error {
	rb.egressMu.RLock()
	reg := rb.egressReg
	rb.egressMu.RUnlock()
	dialer, id, err := reg.Select(requestedID)
	if err != nil {
		rb.setHandshakeError(err)
		return err
	}
	rb.egressMu.Lock()
	rb.dialer = dialer
	rb.egressID = id
	rb.egressMu.Unlock()
	rb.markHandshakeDone()
	rb.logFn("relay: selected egress %q", id)
	return nil
}

func (rb *RelayBridge) markHandshakeDone() {
	rb.handshakeOnce.Do(func() { close(rb.handshakeDone) })
}

func (rb *RelayBridge) setHandshakeError(err error) {
	if err != nil {
		rb.handshakeErr.Store(err.Error())
	}
	rb.markHandshakeDone()
}

func (rb *RelayBridge) waitRequestedEgress(timeout time.Duration) error {
	if rb.mode != "joiner" || rb.requestedEgress() == "" {
		return nil
	}
	select {
	case <-rb.handshakeDone:
		if errVal := rb.handshakeErr.Load(); errVal != nil {
			if msg, ok := errVal.(string); ok {
				return fmt.Errorf("%s", msg)
			}
		}
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("egress selection timed out")
	}
}
func (rb *RelayBridge) send(connID uint32, msgType byte, payload []byte) {
	frame := EncodeFrame(connID, msgType, payload)
	rb.currentTunnel().SendData(frame)
}

func (rb *RelayBridge) handleTunnelData(data []byte) {
	DecodeFrames(data, func(connID uint32, msgType byte, payload []byte) {
		if connID == ControlConnID && msgType == MsgConfig {
			fps, batch, trackCount, ok := DecodeVP8Config(payload)
			if !ok {
				return
			}
			if rb.mode == "creator" {
				rb.logFn("relay: peer requested vp8 pacing fps=%d batch=%d trackCount=%d", fps, batch, trackCount)
				rb.currentTunnel().Reconfigure(fps, batch)
				rb.send(ControlConnID, MsgConfigAck, nil)
				rb.onPeerConfigMu.Lock()
				cb := rb.onPeerConfig
				rb.onPeerConfigMu.Unlock()
				if cb != nil {
					cb(fps, batch, trackCount)
				}
			}
			return
		}
		if connID == ControlConnID && msgType == MsgConfigAck {
			if rb.mode == "joiner" {
				rb.onConfigAckMu.Lock()
				cb := rb.onConfigAck
				rb.onConfigAckMu.Unlock()
				if cb != nil {
					cb()
				}
			}
			return
		}
		if connID == ControlConnID && msgType == MsgClientHello {
			if rb.mode == "creator" {
				hello, ok := DecodeClientHello(payload)
				if !ok {
					rb.send(ControlConnID, MsgControlErr, EncodeControlErrorPayload("bad_client_hello", "invalid egress handshake"))
					return
				}
				if err := rb.selectCreatorEgress(hello.RequestedEgressID); err != nil {
					rb.logFn("relay: egress selection failed: %v", err)
					rb.send(ControlConnID, MsgControlErr, EncodeControlErrorPayload("egress_unavailable", err.Error()))
					return
				}
				_, id, _ := rb.selectedDialer()
				rb.send(ControlConnID, MsgServerHello, EncodeServerHelloPayload(id))
			}
			return
		}
		if connID == ControlConnID && msgType == MsgServerHello {
			if rb.mode == "joiner" {
				hello, ok := DecodeServerHello(payload)
				if !ok {
					rb.setHandshakeError(fmt.Errorf("invalid server egress handshake"))
					return
				}
				rb.egressMu.Lock()
				rb.egressID = hello.SelectedEgressID
				rb.egressMu.Unlock()
				rb.logFn("relay: server selected egress %q", hello.SelectedEgressID)
				rb.markHandshakeDone()
				rb.send(ControlConnID, MsgEgressListRequest, nil)
			}
			return
		}
		if connID == ControlConnID && msgType == MsgControlErr {
			if rb.mode == "joiner" {
				ctrlErr, ok := DecodeControlError(payload)
				if !ok {
					rb.setHandshakeError(fmt.Errorf("creator rejected egress selection"))
					return
				}
				rb.setHandshakeError(fmt.Errorf("%s: %s", ctrlErr.Code, ctrlErr.SafeMessage))
				rb.logFn("relay: control error %s: %s", ctrlErr.Code, ctrlErr.SafeMessage)
				rb.onControlErrorMu.Lock()
				cb := rb.onControlError
				rb.onControlErrorMu.Unlock()
				if cb != nil {
					cb(ctrlErr)
				}
			}
			return
		}
		if connID == ControlConnID && msgType == MsgEgressListRequest {
			if rb.mode == "creator" {
				descriptors := rb.currentEgressRegistry().Descriptors()
				items := make([]EgressDescriptor, 0, len(descriptors))
				for _, descriptor := range descriptors {
					items = append(items, EgressDescriptor{ID: descriptor.ID, IsDefault: descriptor.IsDefault})
				}
				rb.send(ControlConnID, MsgEgressList, EncodeEgressListPayload(items))
			}
			return
		}
		if connID == ControlConnID && msgType == MsgEgressList {
			if rb.mode == "joiner" {
				list, ok := DecodeEgressList(payload)
				if !ok {
					rb.logFn("relay: invalid egress discovery response")
					return
				}
				rb.logFn("EGRESS_LIST:%s", payload)
				for _, descriptor := range list.Egresses {
					rb.send(ControlConnID, MsgEgressProbeRequest, EncodeEgressProbeRequestPayload(descriptor.ID))
				}
			}
			return
		}
		if connID == ControlConnID && msgType == MsgEgressProbeRequest {
			if rb.mode == "creator" {
				request, ok := DecodeEgressProbeRequest(payload)
				if !ok {
					return
				}
				go rb.probeEgress(request.ID)
			}
			return
		}
		if connID == ControlConnID && msgType == MsgEgressProbeResult {
			if rb.mode == "joiner" {
				if _, ok := DecodeEgressProbeResult(payload); ok {
					rb.logFn("EGRESS_PROBE:%s", payload)
				}
			}
			return
		}
		if connID == ControlConnID && msgType == MsgSessionCreate {
			if rb.mode == "creator" {
				request, ok := DecodeSessionCreate(payload)
				if !ok {
					rb.send(ControlConnID, MsgControlErr, EncodeControlErrorPayload("bad_session_request", "invalid session request"))
					return
				}
				rb.onSessionCreateMu.Lock()
				cb := rb.onSessionCreate
				rb.onSessionCreateMu.Unlock()
				if cb == nil {
					rb.send(ControlConnID, MsgControlErr, EncodeControlErrorPayload("session_control_unavailable", "session control is not available"))
					return
				}
				session, err := cb(request)
				if err != nil {
					rb.logFn("relay: session create failed: %v", err)
					rb.send(ControlConnID, MsgControlErr, EncodeControlErrorPayload("session_create_failed", common.MaskError(err)))
					return
				}
				rb.send(ControlConnID, MsgSessionReady, EncodeSessionReadyPayload(session))
			}
			return
		}
		if connID == ControlConnID && msgType == MsgSessionReady {
			if rb.mode == "joiner" {
				session, ok := DecodeSessionReady(payload)
				if !ok {
					rb.logFn("relay: invalid session ready response")
					return
				}
				rb.onSessionReadyMu.Lock()
				cb := rb.onSessionReady
				rb.onSessionReadyMu.Unlock()
				if cb != nil {
					cb(session)
				}
			}
			return
		}
		if connID == ControlConnID && msgType == MsgCookieSubmit {
			if rb.mode == "creator" {
				cookie, ok := DecodeCookieSubmit(payload)
				if !ok {
					rb.send(ControlConnID, MsgControlErr, EncodeControlErrorPayload("bad_cookie_submit", "invalid cookie payload"))
					return
				}
				rb.onCookieSubmitMu.Lock()
				cb := rb.onCookieSubmit
				rb.onCookieSubmitMu.Unlock()
				if cb == nil {
					rb.send(ControlConnID, MsgControlErr, EncodeControlErrorPayload("cookie_vault_unavailable", "cookie storage is not available"))
					return
				}
				ack, err := cb(cookie)
				if err != nil {
					rb.logFn("relay: cookie submit failed platform=%q request=%q: %s", cookie.Platform, cookie.RequestID, common.MaskError(err))
					rb.send(ControlConnID, MsgControlErr, EncodeControlErrorPayload("cookie_submit_failed", common.MaskError(err)))
					return
				}
				rb.send(ControlConnID, MsgCookieAck, EncodeCookieAckPayload(ack))
			}
			return
		}
		if connID == ControlConnID && msgType == MsgCookieAck {
			if rb.mode == "joiner" {
				ack, ok := DecodeCookieAck(payload)
				if !ok {
					rb.logFn("relay: invalid cookie ack")
					return
				}
				rb.onCookieAckMu.Lock()
				cb := rb.onCookieAck
				rb.onCookieAckMu.Unlock()
				if cb != nil {
					cb(ack)
				}
			}
			return
		}
		switch rb.mode {
		case "joiner":
			rb.handleJoinerMessage(connID, msgType, payload)
		case "creator":
			rb.handleCreatorMessage(connID, msgType, payload)
		}
	})
}

func (rb *RelayBridge) handleJoinerMessage(connID uint32, msgType byte, payload []byte) {
	if msgType == MsgUDPReply {
		uval, ok := rb.udpClients.Load(connID)
		if !ok {
			if _, alreadyNacked := rb.nackedConns.LoadOrStore(connID, struct{}{}); !alreadyNacked {
				rb.send(connID, MsgClose, nil)
			}
			return
		}
		uc := uval.(*udpClient)
		reply := make([]byte, len(uc.socksHdr)+len(payload))
		copy(reply, uc.socksHdr)
		copy(reply[len(uc.socksHdr):], payload)
		uc.udpConn.WriteToUDP(reply, uc.clientAddr)
		return
	}
	val, ok := rb.conns.Load(connID)
	if !ok {
		if msgType == MsgClose {
			if uval, ok := rb.udpClients.LoadAndDelete(connID); ok {
				if uc, ok := uval.(*udpClient); ok {
					rb.udpSessions.Delete(uc.mapKey)
				}
			}
			rb.nackedConns.Delete(connID)
		} else if msgType == MsgData {
			if _, alreadyNacked := rb.nackedConns.LoadOrStore(connID, struct{}{}); !alreadyNacked {
				rb.logFn("relay[joiner]: drop msgType=%d for unknown conn %d (payload=%dB), NACK once", msgType, connID, len(payload))
				rb.send(connID, MsgClose, nil)
			}
		} else {
			rb.logFn("relay[joiner]: drop msgType=%d for unknown conn %d (payload=%dB)", msgType, connID, len(payload))
		}
		return
	}
	sc := val.(*socksConn)
	switch msgType {
	case MsgConnectOK:
		select {
		case sc.rdy <- nil:
		default:
			rb.logFn("relay[joiner]: MsgConnectOK %d: rdy already signalled (duplicate)", connID)
		}
	case MsgConnectErr:
		select {
		case sc.rdy <- fmt.Errorf("%s", payload):
		default:
			rb.logFn("relay[joiner]: MsgConnectErr %d: rdy already signalled (duplicate)", connID)
		}
	case MsgData:
		if _, err := sc.conn.Write(payload); err != nil {
			rb.logFn("relay[joiner]: write to socks %d failed: %s", connID, common.MaskError(err))
		}
	case MsgClose:
		sc.conn.Close()
		rb.conns.Delete(connID)
	}
}

func (rb *RelayBridge) handleCreatorMessage(connID uint32, msgType byte, payload []byte) {
	switch msgType {
	case MsgConnect:
		go rb.connectTCP(connID, string(payload))
	case MsgUDP:
		payloadCopy := make([]byte, len(payload))
		copy(payloadCopy, payload)
		go rb.handleUDP(connID, payloadCopy)
	case MsgData:
		val, ok := rb.conns.Load(connID)
		if !ok {
			rb.logFn("relay[creator]: drop MsgData for unknown conn %d (payload=%dB)", connID, len(payload))
			rb.send(connID, MsgClose, nil)
			return
		}
		if c, ok := val.(net.Conn); ok {
			if _, err := c.Write(payload); err != nil {
				rb.logFn("relay[creator]: write to target %d failed: %s", connID, common.MaskError(err))
			}
		}
	case MsgClose:
		found := false
		if val, ok := rb.conns.LoadAndDelete(connID); ok {
			found = true
			if c, ok := val.(net.Conn); ok {
				c.Close()
			}
		}
		if uval, ok := rb.udpClients.LoadAndDelete(connID); ok {
			found = true
			switch v := uval.(type) {
			case *creatorUDP:
				v.close()
			case *udpClient:
				v.udpConn.Close()
				rb.udpSessions.Delete(v.mapKey)
			}
		}
		if !found {
			rb.logFn("relay[creator]: drop MsgClose for unknown conn %d", connID)
		}
	}
}

func (rb *RelayBridge) handleUDP(connID uint32, payload []byte) {
	if len(payload) < 2 {
		return
	}
	addrLen := int(payload[0])
	if addrLen == 0 || len(payload) < 1+addrLen {
		return
	}
	if bytes.IndexByte(payload[1:1+addrLen], 0) != -1 {
		return
	}
	addr := string(payload[1 : 1+addrLen])
	data := payload[1+addrLen:]

	var egress *creatorUDP
	if val, ok := rb.udpClients.Load(connID); ok {
		existing, ok := val.(*creatorUDP)
		if !ok {
			return
		}
		egress = existing
	} else {
		created, err := rb.dialCreatorUDP(addr)
		if err != nil {
			rb.logFn("relay[creator]: UDP %d open %s failed: %v", connID, common.MaskAddr(addr), err)
			return
		}
		if actual, loaded := rb.udpClients.LoadOrStore(connID, created); loaded {
			created.close()
			existing, ok := actual.(*creatorUDP)
			if !ok {
				return
			}
			egress = existing
		} else {
			egress = created
			if verboseUDPLogging {
				rb.logFn("relay[creator]: UDP %d session opened -> %s payload=%dB", connID, addr, len(data))
			}
			go func(e *creatorUDP, id uint32, target string) {
				defer e.close()
				defer rb.udpClients.Delete(id)
				defer rb.send(id, MsgClose, nil)
				buf := make([]byte, common.UDPBufSize)
				var replies int
				var totalIn int64
				for {
					e.setReadDeadline(time.Now().Add(60 * time.Second))
					n, err := e.read(buf)
					if err != nil {
						if verboseUDPLogging {
							rb.logFn("relay[creator]: UDP %d session %s closed after %d replies, %dB in: %v", id, target, replies, totalIn, err)
						}
						return
					}
					replies++
					totalIn += int64(n)
					if verboseUDPLogging && replies == 1 {
						rb.logFn("relay[creator]: UDP %d first reply %dB from %s", id, n, target)
					}
					rb.send(id, MsgUDPReply, buf[:n])
				}
			}(egress, connID, addr)
		}
	}

	if err := egress.writePacket(data, addr); err != nil {
		rb.logFn("relay[creator]: UDP %d write %s failed: %v", connID, common.MaskAddr(addr), err)
	}
}

func (rb *RelayBridge) dialCreatorUDP(addr string) (*creatorUDP, error) {
	dialer, _, err := rb.selectedDialer()
	if err != nil {
		return nil, err
	}
	sess, err := dialer.UDPAssociate(addr, 10*time.Second)
	if err != nil {
		return nil, err
	}
	return &creatorUDP{session: sess}, nil
}

func (rb *RelayBridge) probeEgress(id string) {
	duration, err := rb.currentEgressRegistry().Probe(id, 5*time.Second)
	result := EgressProbeResult{
		ID:        id,
		Available: err == nil,
		LatencyMS: duration.Milliseconds(),
	}
	if err != nil {
		result.Error = common.MaskError(err)
	}
	rb.send(ControlConnID, MsgEgressProbeResult, EncodeEgressProbeResultPayload(result))
}

func (rb *RelayBridge) connectTCP(connID uint32, addr string) {
	rb.logFn("relay: CONNECT %d -> %s", connID, common.MaskAddr(addr))
	dialer, egressID, err := rb.selectedDialer()
	if err != nil {
		rb.logFn("relay: CONNECT %d failed: %s", connID, common.MaskError(err))
		rb.send(connID, MsgConnectErr, []byte(common.MaskError(err)))
		return
	}
	if egressID == "direct" {
		if host, port, splitErr := net.SplitHostPort(addr); splitErr == nil && host != "" && net.ParseIP(host) == nil {
			rb.logDNS(connID, host, port)
		}
	}
	conn, err := dialer.DialTCP(addr, 10*time.Second)
	if err != nil {
		rb.logFn("relay: CONNECT %d failed: %s", connID, common.MaskError(err))
		rb.send(connID, MsgConnectErr, []byte(common.MaskError(err)))
		return
	}
	rb.conns.Store(connID, conn)
	rb.send(connID, MsgConnectOK, nil)
	rb.logFn("relay: CONNECTED %d -> %s", connID, common.MaskAddr(addr))

	buf := make([]byte, rb.readBuf)
	var totalRead int64
	var reads int
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			rb.send(connID, MsgData, buf[:n])
			totalRead += int64(n)
			reads++
			if reads == 1 {
				rb.logFn("relay: conn %d first read %dB", connID, n)
			}
		}
		if err != nil {
			if err != io.EOF {
				rb.logFn("relay: conn %d read error: %s (read %d times, %dB)", connID, common.MaskError(err), reads, totalRead)
			} else if reads == 0 {
				rb.logFn("relay: conn %d EOF with no data read", connID)
			}
			break
		}
	}
	rb.send(connID, MsgClose, nil)
	rb.conns.Delete(connID)
}

func (rb *RelayBridge) logDNS(connID uint32, host, port string) {
	dnsStart := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	ips, dnsErr := net.DefaultResolver.LookupIPAddr(ctx, host)
	cancel()
	if dnsErr != nil {
		rb.logFn("relay: DNS %d %s failed in %s: %v", connID, host, time.Since(dnsStart), dnsErr)
		return
	}
	rb.logFn("relay: DNS %d %s -> %s port=%s took=%s", connID, host, ipAddrList(ips), port, time.Since(dnsStart))
}

type socksConn struct {
	id   uint32
	conn net.Conn
	rb   *RelayBridge
	rdy  chan error
}

func (rb *RelayBridge) ListenSOCKS(addr string) error {
	if rb.closed.Load() {
		return fmt.Errorf("relay: bridge already closed")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	rb.listenerMu.Lock()
	if rb.closed.Load() {
		rb.listenerMu.Unlock()
		ln.Close()
		return fmt.Errorf("relay: bridge already closed")
	}
	rb.listener = ln
	rb.listenerMu.Unlock()
	rb.logFn("relay: SOCKS5 on %s", addr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if rb.closed.Load() {
				rb.logFn("relay: SOCKS listener stopped (bridge closed)")
				return nil
			}
			rb.logFn("relay: accept error: %v", err)
			continue
		}
		go rb.handleSOCKS(conn)
	}
}

func (rb *RelayBridge) handleSOCKS(conn net.Conn) {
	<-rb.ready
	if rb.closed.Load() {
		conn.Close()
		return
	}
	if err := rb.waitRequestedEgress(8 * time.Second); err != nil {
		rb.logFn("relay: SOCKS rejected before egress selection: %v", err)
		conn.Close()
		return
	}
	buf := make([]byte, common.HandshakeBuf)
	n, err := conn.Read(buf)
	if err != nil || n < 2 || buf[0] != common.Ver {
		conn.Close()
		return
	}
	if !common.NegotiateAuth(conn, buf, n, rb.socksUser, rb.socksPass) {
		conn.Close()
		return
	}
	n, err = conn.Read(buf)
	if err != nil || n < 7 || buf[0] != common.Ver {
		conn.Close()
		return
	}
	cmd := buf[1]
	if cmd == common.CmdUDP {
		if verboseUDPLogging {
			rb.logFn("relay: SOCKS UDP ASSOCIATE from %s", conn.RemoteAddr())
		}
		rb.handleUDPAssociate(conn)
		return
	}
	if cmd != common.CmdTCP {
		conn.Write(common.CmdErr)
		conn.Close()
		return
	}
	host, _, err := common.ParseAddress(buf, n)
	if err != nil {
		conn.Write(common.AddrErr)
		conn.Close()
		return
	}

	hostOnly, _, _ := net.SplitHostPort(host)
	if ip := net.ParseIP(hostOnly); ip != nil && ip.IsUnspecified() {
		conn.Write(common.ConnFail)
		conn.Close()
		return
	}
	// Dial local/private addresses directly instead of tunneling to the creator,
	// which cannot reach the joiner's local network. Disabled for now until
	// there is a real use case for local network access through the proxy. So idk if
	// this is a bug or a feature
	// if ip := net.ParseIP(hostOnly); ip != nil && !ip.IsGlobalUnicast() {
	// 	rb.logFn("relay: SOCKS local dial %s", common.MaskAddr(host))
	// 	target, dialErr := net.DialTimeout("tcp", host, 10*time.Second)
	// 	if dialErr != nil {
	// 		rb.logFn("relay: SOCKS local dial failed: %s", common.MaskError(dialErr))
	// 		conn.Write(common.ConnFail)
	// 		conn.Close()
	// 		return
	// 	}
	// 	conn.Write(common.OK)
	// 	go func() {
	// 		defer target.Close()
	// 		defer conn.Close()
	// 		done := make(chan struct{})
	// 		go func() {
	// 			io.Copy(target, conn)
	// 			close(done)
	// 		}()
	// 		io.Copy(conn, target)
	// 		<-done
	// 	}()
	// 	return
	// }

	id := rb.nextID.Add(1)
	sc := &socksConn{id: id, conn: conn, rb: rb, rdy: make(chan error, 1)}
	rb.conns.Store(id, sc)
	rb.logFn("relay: SOCKS CONNECT %d -> %s", id, common.MaskAddr(host))
	rb.send(id, MsgConnect, []byte(host))

	rdyStart := time.Now()
	select {
	case rdyErr := <-sc.rdy:
		if rdyErr != nil {
			rb.logFn("relay: SOCKS CONNECT %d failed after %s: %s", id, time.Since(rdyStart), common.MaskError(rdyErr))
			conn.Write(common.ConnFail)
			conn.Close()
			rb.conns.Delete(id)
			return
		}
	case <-time.After(20 * time.Second):
		rb.logFn("relay: SOCKS CONNECT %d TIMEOUT after %s waiting for MsgConnectOK", id, time.Since(rdyStart))
		conn.Write(common.ConnFail)
		conn.Close()
		rb.conns.Delete(id)
		return
	}
	conn.Write(common.OK)
	rb.logFn("relay: SOCKS CONNECTED %d -> %s rdy_wait=%s", id, common.MaskAddr(host), time.Since(rdyStart))

	go func() {
		readBuf := make([]byte, rb.readBuf)
		var totalSent int64
		var sends int
		for {
			rn, rerr := conn.Read(readBuf)
			if rn > 0 {
				rb.send(id, MsgData, readBuf[:rn])
				totalSent += int64(rn)
				sends++
				if sends == 1 {
					rb.logFn("relay: SOCKS %d first send %dB to tunnel", id, rn)
				}
			}
			if rerr != nil {
				rb.send(id, MsgClose, nil)
				rb.conns.Delete(id)
				if rerr != io.EOF {
					rb.logFn("relay: SOCKS %d read error: %s (sent %d times, %dB)", id, common.MaskError(rerr), sends, totalSent)
				}
				return
			}
		}
	}()
}

func (rb *RelayBridge) handleUDPAssociate(tcpConn net.Conn) {
	udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		tcpConn.Write(common.GenFail)
		tcpConn.Close()
		return
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		tcpConn.Write(common.GenFail)
		tcpConn.Close()
		return
	}
	localAddr := udpConn.LocalAddr().(*net.UDPAddr)
	reply := []byte{common.Ver, 0x00, 0x00, common.AtypIPv4, 127, 0, 0, 1, 0, 0}
	binary.BigEndian.PutUint16(reply[8:10], uint16(localAddr.Port))
	tcpConn.Write(reply)
	if verboseUDPLogging {
		rb.logFn("relay: SOCKS UDP listener bound on 127.0.0.1:%d ctrl=%s", localAddr.Port, tcpConn.RemoteAddr())
	}

	go func() {
		buf := make([]byte, 1)
		tcpConn.Read(buf)
		udpConn.Close()
	}()

	go func() {
		var sessionKeys []string
		var sessionMu sync.Mutex

		defer udpConn.Close()
		defer tcpConn.Close()
		defer func() {
			sessionMu.Lock()
			defer sessionMu.Unlock()
			if verboseUDPLogging {
				rb.logFn("relay: SOCKS UDP listener 127.0.0.1:%d closing, releasing %d sessions", localAddr.Port, len(sessionKeys))
			}
			for _, k := range sessionKeys {
				if idVal, ok := rb.udpSessions.LoadAndDelete(k); ok {
					id := idVal.(uint32)
					rb.udpClients.Delete(id)
					rb.send(id, MsgClose, nil)
				}
			}
		}()

		buf := make([]byte, common.UDPBufSize)
		for {
			n, addr, err := udpConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 10 {
				continue
			}
			frag := buf[2]
			if frag != 0 {
				continue
			}
			dstAddr, headerLen, addrErr := common.ParseAddress(buf, n)
			if addrErr != nil {
				continue
			}

			mapKey := addr.String() + "|" + dstAddr
			var id uint32
			if idVal, ok := rb.udpSessions.Load(mapKey); ok {
				id = idVal.(uint32)
			} else {
				id = rb.nextID.Add(1)
				hdrCopy := make([]byte, headerLen)
				copy(hdrCopy, buf[:headerLen])
				rb.udpClients.Store(id, &udpClient{
					udpConn:    udpConn,
					clientAddr: addr,
					socksHdr:   hdrCopy,
					mapKey:     mapKey,
				})
				rb.udpSessions.Store(mapKey, id)
				sessionMu.Lock()
				sessionKeys = append(sessionKeys, mapKey)
				sessionMu.Unlock()
				if verboseUDPLogging {
					rb.logFn("relay[joiner]: UDP session %d (%s -> %s) opened, first packet %dB", id, addr, dstAddr, n-headerLen)
				}
			}

			payload := make([]byte, len(dstAddr)+1+n-headerLen)
			payload[0] = byte(len(dstAddr))
			copy(payload[1:], dstAddr)
			copy(payload[1+len(dstAddr):], buf[headerLen:n])
			rb.send(id, MsgUDP, payload)
		}
	}()
}

func ipAddrList(ips []net.IPAddr) string {
	if len(ips) == 0 {
		return "[]"
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return "[" + strings.Join(out, ",") + "]"
}
