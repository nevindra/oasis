package sandbox_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nevindra/oasis/sandbox"
)

// mockSandbox is a minimal Sandbox for testing Lazy behavior.
type mockSandbox struct {
	closed atomic.Bool
}

func (m *mockSandbox) Shell(ctx context.Context, req sandbox.ShellRequest) (sandbox.ShellResult, error) {
	return sandbox.ShellResult{Output: "ok", ExitCode: 0}, nil
}
func (m *mockSandbox) ExecCode(context.Context, sandbox.CodeRequest) (sandbox.CodeResult, error) {
	return sandbox.CodeResult{}, nil
}
func (m *mockSandbox) ReadFile(context.Context, sandbox.ReadFileRequest) (sandbox.FileContent, error) {
	return sandbox.FileContent{}, nil
}
func (m *mockSandbox) WriteFile(context.Context, sandbox.WriteFileRequest) error { return nil }
func (m *mockSandbox) UploadFile(context.Context, string, io.Reader) error {
	return nil
}
func (m *mockSandbox) DownloadFile(context.Context, string) (io.ReadCloser, error) {
	return nil, nil
}
func (m *mockSandbox) BrowserNavigate(context.Context, string) error { return nil }
func (m *mockSandbox) BrowserScreenshot(context.Context) ([]byte, error) {
	return nil, nil
}
func (m *mockSandbox) BrowserAction(context.Context, sandbox.BrowserAction) (sandbox.BrowserResult, error) {
	return sandbox.BrowserResult{}, nil
}
func (m *mockSandbox) BrowserSnapshot(context.Context, sandbox.SnapshotOpts) (sandbox.PageSnapshot, error) {
	return sandbox.PageSnapshot{}, nil
}
func (m *mockSandbox) BrowserText(context.Context, sandbox.TextOpts) (sandbox.BrowserTextResult, error) {
	return sandbox.BrowserTextResult{}, nil
}
func (m *mockSandbox) BrowserPDF(context.Context) ([]byte, error) { return nil, nil }
func (m *mockSandbox) BrowserEval(context.Context, string) (string, error) {
	return "", nil
}
func (m *mockSandbox) BrowserFind(context.Context, string) (sandbox.BrowserFindResult, error) {
	return sandbox.BrowserFindResult{}, nil
}
func (m *mockSandbox) BrowserWait(context.Context, sandbox.BrowserWaitOpts) (sandbox.BrowserWaitResult, error) {
	return sandbox.BrowserWaitResult{}, nil
}
func (m *mockSandbox) MCPCall(context.Context, sandbox.MCPRequest) (sandbox.MCPResult, error) {
	return sandbox.MCPResult{}, nil
}
func (m *mockSandbox) EditFile(context.Context, sandbox.EditFileRequest) error { return nil }
func (m *mockSandbox) GlobFiles(context.Context, sandbox.GlobRequest) (sandbox.GlobResult, error) {
	return sandbox.GlobResult{}, nil
}
func (m *mockSandbox) GrepFiles(context.Context, sandbox.GrepRequest) (sandbox.GrepResult, error) {
	return sandbox.GrepResult{}, nil
}
func (m *mockSandbox) Tree(context.Context, sandbox.TreeRequest) (sandbox.TreeResult, error) {
	return sandbox.TreeResult{}, nil
}
func (m *mockSandbox) HTTPFetch(context.Context, sandbox.HTTPFetchRequest) (sandbox.HTTPFetchResult, error) {
	return sandbox.HTTPFetchResult{}, nil
}
func (m *mockSandbox) WebSearch(context.Context, sandbox.WebSearchRequest) (sandbox.WebSearchResult, error) {
	return sandbox.WebSearchResult{}, nil
}
func (m *mockSandbox) WorkspaceInfo(context.Context) (sandbox.WorkspaceInfoResult, error) {
	return sandbox.WorkspaceInfoResult{}, nil
}
func (m *mockSandbox) Close() error {
	m.closed.Store(true)
	return nil
}

func TestLazy_DefersCreation(t *testing.T) {
	var created atomic.Int32
	sb := sandbox.Lazy(func(ctx context.Context) (sandbox.Sandbox, error) {
		created.Add(1)
		return &mockSandbox{}, nil
	})

	if created.Load() != 0 {
		t.Fatal("sandbox created before first call")
	}

	res, err := sb.Shell(context.Background(), sandbox.ShellRequest{Command: "echo hi"})
	if err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if res.Output != "ok" {
		t.Errorf("Shell output = %q, want %q", res.Output, "ok")
	}
	if created.Load() != 1 {
		t.Errorf("expected 1 creation, got %d", created.Load())
	}
}

func TestLazy_CreatesOnce(t *testing.T) {
	var created atomic.Int32
	sb := sandbox.Lazy(func(ctx context.Context) (sandbox.Sandbox, error) {
		created.Add(1)
		return &mockSandbox{}, nil
	})

	for i := 0; i < 10; i++ {
		_, _ = sb.Shell(context.Background(), sandbox.ShellRequest{Command: "echo"})
	}

	if created.Load() != 1 {
		t.Errorf("expected 1 creation across 10 calls, got %d", created.Load())
	}
}

func TestLazy_ConcurrentSafe(t *testing.T) {
	var created atomic.Int32
	sb := sandbox.Lazy(func(ctx context.Context) (sandbox.Sandbox, error) {
		created.Add(1)
		return &mockSandbox{}, nil
	})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = sb.Shell(context.Background(), sandbox.ShellRequest{Command: "echo"})
		}()
	}
	wg.Wait()

	if created.Load() != 1 {
		t.Errorf("expected 1 creation across 50 goroutines, got %d", created.Load())
	}
}

func TestLazy_RetriesAfterError(t *testing.T) {
	var calls atomic.Int32
	sb := sandbox.Lazy(func(ctx context.Context) (sandbox.Sandbox, error) {
		n := calls.Add(1)
		if n == 1 {
			return nil, errors.New("provisioning failed")
		}
		return &mockSandbox{}, nil
	})

	_, err := sb.Shell(context.Background(), sandbox.ShellRequest{Command: "echo"})
	if err == nil {
		t.Fatal("expected error on first call")
	}

	res, err := sb.Shell(context.Background(), sandbox.ShellRequest{Command: "echo"})
	if err != nil {
		t.Fatalf("expected success on retry, got: %v", err)
	}
	if res.Output != "ok" {
		t.Errorf("Shell output = %q, want %q", res.Output, "ok")
	}
}

func TestLazy_CloseWithoutCreation(t *testing.T) {
	sb := sandbox.Lazy(func(ctx context.Context) (sandbox.Sandbox, error) {
		t.Fatal("create should not be called")
		return nil, nil
	})

	if err := sb.Close(); err != nil {
		t.Fatalf("Close on uncreated sandbox: %v", err)
	}
}

func TestLazy_CloseForwardsToInner(t *testing.T) {
	mock := &mockSandbox{}
	sb := sandbox.Lazy(func(ctx context.Context) (sandbox.Sandbox, error) {
		return mock, nil
	})

	// Trigger creation.
	_, _ = sb.Shell(context.Background(), sandbox.ShellRequest{Command: "echo"})

	if err := sb.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !mock.closed.Load() {
		t.Error("inner sandbox was not closed")
	}
}
