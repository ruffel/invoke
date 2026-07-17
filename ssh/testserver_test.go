package ssh_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/crypto/ssh"
)

// testServer is an in-process SSH server backed by the host's real shell.
// It exercises the provider's protocol handling — quoting, out-of-band
// env, exit-status and exit-signal extraction, signal delivery — against a
// genuine SSH conversation, without needing a container.
type testServer struct {
	addr      string
	hostKey   ssh.PublicKey
	config    *ssh.ServerConfig
	listener  net.Listener
	closeOnce sync.Once
}

const testPassword = "correct-horse"

// startTestServer launches a server on a random localhost port and stops it
// when the test finishes.
func startTestServer(t *testing.T) *testServer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("host key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}

	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) == testPassword {
				return &ssh.Permissions{}, nil
			}

			return nil, errors.New("invalid password")
		},
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx // Test listener.
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := &testServer{
		addr:     listener.Addr().String(),
		hostKey:  signer.PublicKey(),
		config:   config,
		listener: listener,
	}

	go srv.acceptLoop()

	t.Cleanup(srv.close)

	return srv
}

func (s *testServer) close() {
	s.closeOnce.Do(func() { _ = s.listener.Close() })
}

func (s *testServer) host() string {
	host, _, _ := net.SplitHostPort(s.addr)

	return host
}

func (s *testServer) port() int {
	_, portStr, _ := net.SplitHostPort(s.addr)
	port, _ := strconv.Atoi(portStr)

	return port
}

func (s *testServer) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // Listener closed.
		}

		go s.handleConn(conn)
	}
}

func (s *testServer) handleConn(conn net.Conn) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		_ = conn.Close()

		return
	}

	defer func() { _ = sshConn.Close() }()

	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only sessions are supported")

			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}

		go handleSession(channel, requests)
	}
}

// sessionState carries the per-session environment and the running command,
// so a signal request can reach the process. A signal that arrives before
// the command has started (the client can signal immediately after the exec
// request) is buffered and applied once it does.
type sessionState struct {
	env []string

	mu      sync.Mutex
	cmd     *exec.Cmd
	pending os.Signal
}

func (s *sessionState) setCmd(cmd *exec.Cmd) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cmd = cmd
	if s.pending != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(s.pending)
		s.pending = nil
	}
}

func handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	state := &sessionState{}

	for req := range requests {
		switch req.Type {
		case "env":
			state.addEnv(req.Payload)
			reply(req, true)
		case "exec":
			go runExec(channel, state, execCommand(req.Payload))

			reply(req, true)
		case "signal":
			state.forwardSignal(signalName(req.Payload))
			reply(req, true)
		default:
			// pty-req, shell, subsystem, and the rest are unsupported.
			reply(req, false)
		}
	}
}

func (s *sessionState) addEnv(payload []byte) {
	name, rest := readString(payload)
	value, _ := readString(rest)
	s.env = append(s.env, name+"="+value)
}

func (s *sessionState) forwardSignal(name string) {
	sig, ok := signalToSyscall(name)
	if !ok {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(sig)

		return
	}

	// The command has not started yet; apply the signal when it does.
	s.pending = sig
}

// runExec runs the requested command through the host shell, wiring the
// channel to its streams, then reports the exit status or signal.
func runExec(channel ssh.Channel, state *sessionState, command string) {
	cmd := exec.Command("/bin/sh", "-c", command) //nolint:noctx // Test server runs the requested command; lifetime is the session.

	cmd.Env = append(os.Environ(), state.env...)
	cmd.Stdout = channel
	cmd.Stderr = channel.Stderr()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		exitWith(channel, 1)

		return
	}

	if err := cmd.Start(); err != nil {
		exitWith(channel, 127)

		return
	}

	state.setCmd(cmd)

	go func() {
		_, _ = io.Copy(stdin, channel)
		_ = stdin.Close()
	}()

	reportExit(channel, cmd.Wait())
}

// reportExit sends the SSH exit-status or exit-signal for a finished
// command, mirroring what a real sshd does.
func reportExit(channel ssh.Channel, waitErr error) {
	defer func() { _ = channel.Close() }()

	if waitErr == nil {
		exitWith(channel, 0)

		return
	}

	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		exitWith(channel, 1)

		return
	}

	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if ok && status.Signaled() {
		exitBySignal(channel, sysToSignalName(status.Signal()))

		return
	}

	exitWith(channel, exitErr.ExitCode())
}

func exitWith(channel ssh.Channel, code int) {
	payload := struct{ Status uint32 }{Status: uint32(code)} //nolint:gosec // Exit codes are small non-negative values.
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&payload))
}

func exitBySignal(channel ssh.Channel, name string) {
	payload := struct {
		Signal     string
		CoreDumped bool
		Error      string
		Lang       string
	}{Signal: name}
	_, _ = channel.SendRequest("exit-signal", false, ssh.Marshal(&payload))
}

func reply(req *ssh.Request, ok bool) {
	if req.WantReply {
		_ = req.Reply(ok, nil)
	}
}

// execCommand extracts the command string from an exec request payload.
func execCommand(payload []byte) string {
	cmd, _ := readString(payload)

	return cmd
}

// signalName extracts the signal name from a signal request payload.
func signalName(payload []byte) string {
	name, _ := readString(payload)

	return name
}

// readString reads an SSH wire string (uint32 length prefix) from b.
func readString(b []byte) (string, []byte) {
	if len(b) < 4 {
		return "", nil
	}

	n := binary.BigEndian.Uint32(b)
	if int(n) > len(b)-4 {
		return "", nil
	}

	return string(b[4 : 4+n]), b[4+n:]
}
