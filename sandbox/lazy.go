package sandbox

import (
	"context"
	"io"
	"sync"
)

// Lazy returns a Sandbox that defers creation until the first method call.
// The create function is called at most once; subsequent calls reuse the
// same instance. If create returns an error, it is retried on the next call.
//
// Close is always safe to call — if the sandbox was never created, Close
// is a no-op.
func Lazy(create func(ctx context.Context) (Sandbox, error)) Sandbox {
	return &lazySandbox{create: create}
}

type lazySandbox struct {
	create func(ctx context.Context) (Sandbox, error)
	mu     sync.Mutex
	sb     Sandbox
}

func (l *lazySandbox) get(ctx context.Context) (Sandbox, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sb != nil {
		return l.sb, nil
	}
	sb, err := l.create(ctx)
	if err != nil {
		return nil, err
	}
	l.sb = sb
	return sb, nil
}

func (l *lazySandbox) Shell(ctx context.Context, req ShellRequest) (ShellResult, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return ShellResult{}, err
	}
	return sb.Shell(ctx, req)
}

func (l *lazySandbox) ExecCode(ctx context.Context, req CodeRequest) (CodeResult, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return CodeResult{}, err
	}
	return sb.ExecCode(ctx, req)
}

func (l *lazySandbox) ReadFile(ctx context.Context, req ReadFileRequest) (FileContent, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return FileContent{}, err
	}
	return sb.ReadFile(ctx, req)
}

func (l *lazySandbox) WriteFile(ctx context.Context, req WriteFileRequest) error {
	sb, err := l.get(ctx)
	if err != nil {
		return err
	}
	return sb.WriteFile(ctx, req)
}

func (l *lazySandbox) UploadFile(ctx context.Context, path string, data io.Reader) error {
	sb, err := l.get(ctx)
	if err != nil {
		return err
	}
	return sb.UploadFile(ctx, path, data)
}

func (l *lazySandbox) DownloadFile(ctx context.Context, path string) (io.ReadCloser, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return nil, err
	}
	return sb.DownloadFile(ctx, path)
}

func (l *lazySandbox) BrowserNavigate(ctx context.Context, url string) error {
	sb, err := l.get(ctx)
	if err != nil {
		return err
	}
	return sb.BrowserNavigate(ctx, url)
}

func (l *lazySandbox) BrowserScreenshot(ctx context.Context) ([]byte, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return nil, err
	}
	return sb.BrowserScreenshot(ctx)
}

func (l *lazySandbox) BrowserAction(ctx context.Context, action BrowserAction) (BrowserResult, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return BrowserResult{}, err
	}
	return sb.BrowserAction(ctx, action)
}

func (l *lazySandbox) BrowserSnapshot(ctx context.Context, opts SnapshotOpts) (BrowserSnapshot, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return BrowserSnapshot{}, err
	}
	return sb.BrowserSnapshot(ctx, opts)
}

func (l *lazySandbox) BrowserText(ctx context.Context, opts TextOpts) (BrowserTextResult, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return BrowserTextResult{}, err
	}
	return sb.BrowserText(ctx, opts)
}

func (l *lazySandbox) BrowserPDF(ctx context.Context) ([]byte, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return nil, err
	}
	return sb.BrowserPDF(ctx)
}

func (l *lazySandbox) BrowserEval(ctx context.Context, expression string) (string, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return "", err
	}
	return sb.BrowserEval(ctx, expression)
}

func (l *lazySandbox) BrowserFind(ctx context.Context, query string) (BrowserFindResult, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return BrowserFindResult{}, err
	}
	return sb.BrowserFind(ctx, query)
}

func (l *lazySandbox) BrowserWait(ctx context.Context, opts BrowserWaitOpts) (BrowserWaitResult, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return BrowserWaitResult{}, err
	}
	return sb.BrowserWait(ctx, opts)
}

func (l *lazySandbox) MCPCall(ctx context.Context, req MCPRequest) (MCPResult, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return MCPResult{}, err
	}
	return sb.MCPCall(ctx, req)
}

func (l *lazySandbox) EditFile(ctx context.Context, req EditFileRequest) error {
	sb, err := l.get(ctx)
	if err != nil {
		return err
	}
	return sb.EditFile(ctx, req)
}

func (l *lazySandbox) GlobFiles(ctx context.Context, req GlobRequest) (GlobResult, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return GlobResult{}, err
	}
	return sb.GlobFiles(ctx, req)
}

func (l *lazySandbox) GrepFiles(ctx context.Context, req GrepRequest) (GrepResult, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return GrepResult{}, err
	}
	return sb.GrepFiles(ctx, req)
}

func (l *lazySandbox) Tree(ctx context.Context, req TreeRequest) (TreeResult, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return TreeResult{}, err
	}
	return sb.Tree(ctx, req)
}

func (l *lazySandbox) HTTPFetch(ctx context.Context, req HTTPFetchRequest) (HTTPFetchResult, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return HTTPFetchResult{}, err
	}
	return sb.HTTPFetch(ctx, req)
}

func (l *lazySandbox) WebSearch(ctx context.Context, req WebSearchRequest) (WebSearchResult, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return WebSearchResult{}, err
	}
	return sb.WebSearch(ctx, req)
}

func (l *lazySandbox) WorkspaceInfo(ctx context.Context) (WorkspaceInfoResult, error) {
	sb, err := l.get(ctx)
	if err != nil {
		return WorkspaceInfoResult{}, err
	}
	return sb.WorkspaceInfo(ctx)
}

func (l *lazySandbox) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sb == nil {
		return nil
	}
	return l.sb.Close()
}
