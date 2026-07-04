package android

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"vconnect/relay/common"
)

var (
	stdinOnce        sync.Once
	commandLines     chan string
	resolveResponses chan string
	resolveActive    atomic.Bool
	resolveMu        sync.Mutex
)

const resolveResponseTimeout = 15 * time.Second

func startStdinDispatcher() {
	commandLines = make(chan string, 16)
	resolveResponses = make(chan string, 1)
	rawLines := make(chan string, 16)

	go func() {
		defer close(rawLines)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			rawLines <- strings.TrimSpace(scanner.Text())
		}
	}()

	go func() {
		defer close(commandLines)
		defer close(resolveResponses)
		for line := range rawLines {
			if resolveActive.Load() && !isCommandLine(line) {
				resolveResponses <- line
				continue
			}
			commandLines <- line
		}
	}()
}

func isCommandLine(line string) bool {
	return strings.HasPrefix(line, "JOIN:") ||
		strings.HasPrefix(line, "AUTH:") ||
		strings.HasPrefix(line, "SERVICE_EGRESS:")
}

func ReadStdinLine() (string, error) {
	stdinOnce.Do(startStdinDispatcher)
	line, ok := <-commandLines
	if !ok {
		return "", io.EOF
	}
	return line, nil
}

func RequestResolve(hostname string) (string, error) {
	stdinOnce.Do(startStdinDispatcher)
	resolveMu.Lock()
	defer resolveMu.Unlock()

	resolveActive.Store(true)
	defer resolveActive.Store(false)

	fmt.Printf("RESOLVE:%s\n", hostname)

	select {
	case line, ok := <-resolveResponses:
		if !ok {
			return "", io.EOF
		}
		if line == "" {
			return "", fmt.Errorf("empty resolve for %s", hostname)
		}
		return line, nil
	case <-time.After(resolveResponseTimeout):
		return "", fmt.Errorf("resolve timeout for %s", hostname)
	}
}

type StatusEmitter struct{}

func (StatusEmitter) EmitStatus(status string)   { common.EmitStatus(status) }
func (StatusEmitter) EmitStatusError(msg string) { common.EmitStatusError(msg) }

type PCConfigurer struct{}

func (PCConfigurer) ConfigureSettingEngine(settingEngine *webrtc.SettingEngine) {
	settingEngine.SetNet(&common.AndroidNet{})
}
