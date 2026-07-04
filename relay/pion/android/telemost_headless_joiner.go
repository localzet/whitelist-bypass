package android

import (
	"log"
	"strings"

	"vconnect/relay/common"
	"vconnect/relay/pion"
	joiner "vconnect/relay/pion/headless-joiner-common"
	"vconnect/relay/tunnel"
)

type TelemostHeadlessJoiner struct {
	inner       *joiner.TelemostHeadlessJoiner
	OnConnected func(tunnel.DataTunnel)
	OnCommand   func(string)
}

func NewTelemostHeadlessJoiner(logFn func(string, ...any)) *TelemostHeadlessJoiner {
	if logFn == nil {
		logFn = log.Printf
	}
	inner := joiner.NewTelemostHeadlessJoiner(logFn, RequestResolve, StatusEmitter{}, PCConfigurer{}, pion.AddTunnelTracks, pion.ReadTrack)
	wrapper := &TelemostHeadlessJoiner{inner: inner}
	inner.OnConnected = func(tun tunnel.DataTunnel) {
		if wrapper.OnConnected != nil {
			wrapper.OnConnected(tun)
		}
	}
	return wrapper
}

func (j *TelemostHeadlessJoiner) Run() {
	j.inner.Status.EmitStatus(common.StatusReady)
	for {
		line, err := ReadStdinLine()
		if err != nil {
			log.Printf("telemost-joiner: stdin closed: %v", err)
			return
		}
		if strings.HasPrefix(line, "JOIN:") {
			go j.inner.RunWithParams(strings.TrimPrefix(line, "JOIN:"))
			break
		}
	}
	for {
		line, err := ReadStdinLine()
		if err != nil {
			log.Printf("telemost-joiner: stdin closed: %v", err)
			return
		}
		if j.OnCommand != nil {
			j.OnCommand(line)
		}
	}
}
