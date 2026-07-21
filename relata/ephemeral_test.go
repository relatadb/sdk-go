package relata_test

// EphemeralServer starts a relata serve process on a free port for tests.
//
// Usage:
//
//	func TestMyIntegration(t *testing.T) {
//	    srv := StartEphemeral(t)
//	    _ = srv.BaseURL // "http://127.0.0.1:<port>"
//	    _ = srv.Token   // bearer token
//	}

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

// EphemeralServer holds the address and credentials of a spawned relata process.
type EphemeralServer struct {
	Port    int
	Token   string
	BaseURL string
	cmd     *exec.Cmd
}

// Stop kills the server process (called automatically via t.Cleanup).
func (s *EphemeralServer) Stop() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
}

// StartEphemeral spawns a relata server on a random port and waits for it to
// accept connections. It registers t.Cleanup to kill the process when the test ends.
//
// Set RELATA_BIN to override the binary path (default: "relata").
// Set RELATA_TEST_TOKEN to override the bearer token (default: "relata-test").
func StartEphemeral(t *testing.T) *EphemeralServer {
	t.Helper()

	port, err := freePort()
	if err != nil {
		t.Fatalf("StartEphemeral: pick free port: %v", err)
	}

	token := os.Getenv("RELATA_TEST_TOKEN")
	if token == "" {
		token = "relata-test"
	}
	bin := os.Getenv("RELATA_BIN")
	if bin == "" {
		bin = "relata"
	}

	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("RELATA_PORT=%d", port),
		fmt.Sprintf("RELATA_BEARER_TOKEN=%s", token),
		"RELATA_LOG_LEVEL=warn",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("StartEphemeral: start %q: %v", bin, err)
	}

	srv := &EphemeralServer{
		Port:    port,
		Token:   token,
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		cmd:     cmd,
	}
	t.Cleanup(srv.Stop)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return srv
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("StartEphemeral: relata on port %d did not start within 10 s", port)
	return nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}
