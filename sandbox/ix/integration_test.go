//go:build integration

package ix

import (
	"context"
	"testing"
	"time"

	"github.com/nevindra/oasis/sandbox"
)

func TestIntegrationCreateAndShell(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewManager(ctx, ManagerConfig{
		Image:      "oasis-ix:latest",
		DefaultTTL: 2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	sb, err := mgr.Create(ctx, sandbox.CreateOpts{
		SessionID: "integration-test",
		TTL:       2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	// Shell
	result, err := sb.Shell(ctx, sandbox.ShellRequest{Command: "echo hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", result.ExitCode, result.Output)
	}
	t.Logf("shell output: %s", result.Output)

	// Code execution
	codeResult, err := sb.ExecCode(ctx, sandbox.CodeRequest{
		Language: "python",
		Code:     "print(1 + 1)",
	})
	if err != nil {
		t.Fatal(err)
	}
	if codeResult.Status != "ok" {
		t.Fatalf("expected ok, got %s: %s", codeResult.Status, codeResult.Stderr)
	}
	t.Logf("code output: %s", codeResult.Stdout)

	// File write + read roundtrip
	err = sb.WriteFile(ctx, sandbox.WriteFileRequest{
		Path:    "/tmp/test.txt",
		Content: "hello world",
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := sb.ReadFile(ctx, "/tmp/test.txt")
	if err != nil {
		t.Fatal(err)
	}
	if content.Content != "hello world" {
		t.Fatalf("expected 'hello world', got %q", content.Content)
	}

	// Get by session ID
	sb2, err := mgr.Get("integration-test")
	if err != nil {
		t.Fatal(err)
	}
	if sb2 == nil {
		t.Fatal("expected sandbox from Get")
	}
}
