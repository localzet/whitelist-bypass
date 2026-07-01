package tunnel

import (
	"encoding/binary"
	"encoding/json"
)

const (
	MsgConnect            byte = 0x01
	MsgConnectOK          byte = 0x02
	MsgConnectErr         byte = 0x03
	MsgData               byte = 0x04
	MsgClose              byte = 0x05
	MsgUDP                byte = 0x06
	MsgUDPReply           byte = 0x07
	MsgConfig             byte = 0x08
	MsgConfigAck          byte = 0x09
	MsgClientHello        byte = 0x0A
	MsgServerHello        byte = 0x0B
	MsgControlErr         byte = 0x0C
	MsgEgressListRequest  byte = 0x0D
	MsgEgressList         byte = 0x0E
	MsgEgressProbeRequest byte = 0x0F
	MsgEgressProbeResult  byte = 0x10
)

const ControlConnID uint32 = 0
const ControlProtocolVersion = 1
const MaxControlPayload = 4096

type DataTunnel interface {
	SendData(data []byte)
	SetOnData(fn func([]byte))
	SetOnClose(fn func())
	Reconfigure(fps, batch int)
}

type ClientHello struct {
	ProtocolVersion  int      `json:"protocolVersion"`
	Capabilities     []string `json:"capabilities,omitempty"`
	RequestedEgressID string   `json:"requestedEgressId,omitempty"`
}

type ServerHello struct {
	ProtocolVersion int      `json:"protocolVersion"`
	Capabilities    []string `json:"capabilities,omitempty"`
	SelectedEgressID string   `json:"selectedEgressId"`
}

type ControlError struct {
	Code        string `json:"code"`
	SafeMessage string `json:"safeMessage"`
}

type EgressDescriptor struct {
	ID        string `json:"id"`
	IsDefault bool   `json:"isDefault"`
}

type EgressList struct {
	Egresses []EgressDescriptor `json:"egresses"`
}

type EgressProbeRequest struct {
	ID string `json:"id"`
}

type EgressProbeResult struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
	LatencyMS int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
}

func EncodeClientHello(requestedEgressID string) []byte {
	return EncodeFrame(ControlConnID, MsgClientHello, EncodeClientHelloPayload(requestedEgressID))
}

func EncodeClientHelloPayload(requestedEgressID string) []byte {
	return encodeControlJSON(ClientHello{
		ProtocolVersion:  ControlProtocolVersion,
		Capabilities:     []string{"egress-select", "egress-discovery", "egress-probe"},
		RequestedEgressID: requestedEgressID,
	})
}

func EncodeServerHello(selectedEgressID string) []byte {
	return EncodeFrame(ControlConnID, MsgServerHello, EncodeServerHelloPayload(selectedEgressID))
}

func EncodeServerHelloPayload(selectedEgressID string) []byte {
	return encodeControlJSON(ServerHello{
		ProtocolVersion: ControlProtocolVersion,
		Capabilities:    []string{"egress-select", "egress-discovery", "egress-probe"},
		SelectedEgressID: selectedEgressID,
	})
}

func EncodeControlError(code, safeMessage string) []byte {
	return EncodeFrame(ControlConnID, MsgControlErr, EncodeControlErrorPayload(code, safeMessage))
}

func EncodeControlErrorPayload(code, safeMessage string) []byte {
	return encodeControlJSON(ControlError{Code: code, SafeMessage: safeMessage})
}

func DecodeClientHello(payload []byte) (ClientHello, bool) {
	var msg ClientHello
	if len(payload) == 0 || len(payload) > MaxControlPayload {
		return msg, false
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return msg, false
	}
	return msg, msg.ProtocolVersion == ControlProtocolVersion
}

func DecodeServerHello(payload []byte) (ServerHello, bool) {
	var msg ServerHello
	if len(payload) == 0 || len(payload) > MaxControlPayload {
		return msg, false
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return msg, false
	}
	return msg, msg.ProtocolVersion == ControlProtocolVersion
}

func DecodeControlError(payload []byte) (ControlError, bool) {
	var msg ControlError
	if len(payload) == 0 || len(payload) > MaxControlPayload {
		return msg, false
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return msg, false
	}
	return msg, msg.Code != ""
}

func EncodeEgressListPayload(egresses []EgressDescriptor) []byte {
	return encodeControlJSON(EgressList{Egresses: egresses})
}

func DecodeEgressList(payload []byte) (EgressList, bool) {
	var msg EgressList
	if !decodeControlJSON(payload, &msg) {
		return msg, false
	}
	return msg, len(msg.Egresses) <= 64
}

func EncodeEgressProbeRequestPayload(id string) []byte {
	return encodeControlJSON(EgressProbeRequest{ID: id})
}

func DecodeEgressProbeRequest(payload []byte) (EgressProbeRequest, bool) {
	var msg EgressProbeRequest
	if !decodeControlJSON(payload, &msg) {
		return msg, false
	}
	return msg, msg.ID != ""
}

func EncodeEgressProbeResultPayload(result EgressProbeResult) []byte {
	return encodeControlJSON(result)
}

func DecodeEgressProbeResult(payload []byte) (EgressProbeResult, bool) {
	var msg EgressProbeResult
	if !decodeControlJSON(payload, &msg) {
		return msg, false
	}
	return msg, msg.ID != "" && msg.LatencyMS >= 0
}

func decodeControlJSON(payload []byte, value any) bool {
	if len(payload) == 0 || len(payload) > MaxControlPayload {
		return false
	}
	return json.Unmarshal(payload, value) == nil
}

func encodeControlJSON(value any) []byte {
	payload, _ := json.Marshal(value)
	return payload
}

func EncodeVP8Config(fps, batch, trackCount int) []byte {
	if fps < 1 {
		fps = 1
	}
	if batch < 1 {
		batch = 1
	}
	if trackCount < 1 {
		trackCount = 1
	}
	if fps > 0xFFFF {
		fps = 0xFFFF
	}
	if batch > 0xFFFF {
		batch = 0xFFFF
	}
	if trackCount > 0xFFFF {
		trackCount = 0xFFFF
	}
	var payload [6]byte
	binary.BigEndian.PutUint16(payload[0:2], uint16(fps))
	binary.BigEndian.PutUint16(payload[2:4], uint16(batch))
	binary.BigEndian.PutUint16(payload[4:6], uint16(trackCount))
	return EncodeFrame(ControlConnID, MsgConfig, payload[:])
}

func DecodeVP8Config(payload []byte) (fps, batch, trackCount int, ok bool) {
	if len(payload) < 4 {
		return 0, 0, 0, false
	}
	fps = int(binary.BigEndian.Uint16(payload[0:2]))
	batch = int(binary.BigEndian.Uint16(payload[2:4]))
	trackCount = 1
	if len(payload) >= 6 {
		trackCount = int(binary.BigEndian.Uint16(payload[4:6]))
	}
	return fps, batch, trackCount, true
}

func EncodeFrame(connID uint32, msgType byte, payload []byte) []byte {
	buf := make([]byte, 4+5+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], uint32(5+len(payload)))
	binary.BigEndian.PutUint32(buf[4:8], connID)
	buf[8] = msgType
	copy(buf[9:], payload)
	return buf
}

func DecodeFrames(data []byte, cb func(connID uint32, msgType byte, payload []byte)) {
	for len(data) >= 4 {
		frameLen := int(binary.BigEndian.Uint32(data[0:4]))
		if frameLen < 5 || 4+frameLen > len(data) {
			return
		}
		connID := binary.BigEndian.Uint32(data[4:8])
		msgType := data[8]
		payload := data[9 : 4+frameLen]
		cb(connID, msgType, payload)
		data = data[4+frameLen:]
	}
}
