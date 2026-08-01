// Command ephemeral_server spawns a temporary `relata serve`, runs a health
// check, then tears down.
//
// Requires the `relata` binary on $PATH (or RELATA_BIN).
//
// Usage:
//
//	go run ./examples/ephemeral_server
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/relatadb/sdk-go/v2/relata"
)

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitForPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			c.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("server on port %d did not start within %s", port, timeout)
}

func main() {
	bin := flag.String("bin", getenv("RELATA_BIN", "relata"), "path to relata binary")
	flag.Parse()

	port, err := freePort()
	if err != nil {
		fmt.Printf("freePort: %v\n", err)
		os.Exit(1)
	}
	token := "relata-test"

	fmt.Printf("Spawning %s serve on port %d ...\n", *bin, port)
	cmd := exec.Command(*bin, "serve")
	cmd.Env = append(os.Environ(),
		"RELATA_PORT="+fmt.Sprint(port),
		"RELATA_BEARER_TOKEN="+token,
		"RELATA_LOG_LEVEL=warn",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Printf("spawn: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	if err := waitForPort(port, 10*time.Second); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	fmt.Printf("Server ready at %s\n", url)

	client := relata.New(url, &relata.ClientOptions{
		BearerToken:    token,
		DefaultPurpose: "analytics",
		Timeout:        10 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	health, err := client.Health(ctx)
	if err != nil {
		fmt.Printf("health: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Health: status=%s  profile=%s  node=%s\n",
		health.Status, health.Profile, health.NodeID)
}

func getenv(key, dflt string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return dflt
}
