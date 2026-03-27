package ixd

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// internalToken is a fixed auth token for ix daemon → Pinchtab communication.
// Pinchtab requires a token when PINCHTAB_CONFIG is set. Since communication
// is localhost-only inside the container, this token is not a security boundary.
const internalToken = "ix-internal"

// pinchtab manages the Pinchtab bridge subprocess lifecycle.
// It spawns "pinchtab bridge" on startup if the binary is found in PATH,
// polls its health endpoint, and restarts on crash (up to maxRestarts).
type pinchtab struct {
	port        string
	cmd         *exec.Cmd
	available   bool
	mu          sync.Mutex
	restarts    int
	maxRestarts int
	cancel      context.CancelFunc
}

// newPinchtab attempts to start a Pinchtab bridge subprocess.
// Returns a pinchtab instance. If the pinchtab binary is not in PATH,
// available is false and all browser proxy calls should return 501.
func newPinchtab(ctx context.Context) *pinchtab {
	p := &pinchtab{
		port:        "9867",
		maxRestarts: 3,
	}

	if _, err := exec.LookPath("pinchtab"); err != nil {
		log.Printf("pinchtab not in PATH, browser disabled")
		return p
	}

	if err := p.start(ctx); err != nil {
		log.Printf("pinchtab start failed: %v", err)
		return p
	}

	return p
}

// writeConfig writes a Pinchtab config that disables auth and IDPI,
// returning the path to the config file. Pinchtab auto-generates a random
// auth token on first run; since the ix daemon is the only client and
// communication is localhost-only, we disable auth entirely.
func (p *pinchtab) writeConfig() (string, error) {
	dir := filepath.Join(os.TempDir(), "pinchtab")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "config.json")
	config := []byte(`{
  "server": {"port": "` + p.port + `", "bind": "127.0.0.1", "token": "` + internalToken + `"},
  "instanceDefaults": {"mode": "headless"},
  "security": {"idpi": {"enabled": false}}
}`)
	if err := os.WriteFile(path, config, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// start spawns the pinchtab bridge subprocess and waits for it to become healthy.
func (p *pinchtab) start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	configPath, err := p.writeConfig()
	if err != nil {
		return fmt.Errorf("write pinchtab config: %w", err)
	}

	childCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel

	p.cmd = exec.CommandContext(childCtx, "pinchtab", "bridge")
	p.cmd.Env = append(os.Environ(), "PINCHTAB_CONFIG="+configPath)
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := p.cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("spawn pinchtab: %w", err)
	}

	// Monitor process exit in background.
	go func() {
		_ = p.cmd.Wait()
	}()

	if err := p.waitHealthy(ctx); err != nil {
		p.kill()
		return fmt.Errorf("pinchtab not healthy: %w", err)
	}

	p.available = true
	log.Printf("pinchtab bridge ready on :%s", p.port)
	return nil
}

// waitHealthy polls the Pinchtab health endpoint with exponential backoff.
func (p *pinchtab) waitHealthy(ctx context.Context) error {
	client := &http.Client{Timeout: 2 * time.Second}
	endpoint := fmt.Sprintf("http://127.0.0.1:%s/health", p.port)

	deadline := time.After(15 * time.Second)
	delay := 200 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("pinchtab not ready after 15s")
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			time.Sleep(delay)
			delay = min(delay*2, 2*time.Second)
			continue
		}
		req.Header.Set("Authorization", "Bearer "+internalToken)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(delay)
		delay = min(delay*2, 2*time.Second)
	}
}

// restart attempts to restart the Pinchtab subprocess after a crash.
// Returns an error if max restarts exceeded.
func (p *pinchtab) restart(ctx context.Context) error {
	p.mu.Lock()
	p.restarts++
	count := p.restarts
	p.available = false
	p.mu.Unlock()

	if count > p.maxRestarts {
		return fmt.Errorf("pinchtab max restarts (%d) exceeded", p.maxRestarts)
	}

	log.Printf("pinchtab restart attempt %d/%d", count, p.maxRestarts)
	p.kill()
	return p.start(ctx)
}

// kill terminates the Pinchtab subprocess and its children.
func (p *pinchtab) kill() {
	if p.cancel != nil {
		p.cancel()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
}

// baseURL returns the Pinchtab bridge HTTP base URL.
func (p *pinchtab) baseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%s", p.port)
}

// isAvailable returns true if Pinchtab is running and healthy.
func (p *pinchtab) isAvailable() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.available
}

// shutdown gracefully stops the Pinchtab subprocess.
func (p *pinchtab) shutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.available = false
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_ = p.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			p.kill()
		}
	}
}
