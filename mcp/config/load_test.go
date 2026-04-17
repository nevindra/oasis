package mcpconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nevindra/oasis"
)

func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	cfgDir := filepath.Join(dir, ".oasis")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfgDir, "mcp.json")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_Empty(t *testing.T) {
	dir := t.TempDir()
	cfgs, err := Load(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(cfgs) != 0 {
		t.Errorf("expected 0, got %d", len(cfgs))
	}
}

func TestLoad_StdioServer(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{
        "version": 1,
        "mcpServers": {
            "fs": {
                "command": "npx",
                "args": ["-y", "fs-server"],
                "env": {"FS_RO": "1"}
            }
        }
    }`)
	cfgs, err := Load(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("expected 1 cfg, got %d", len(cfgs))
	}
	s, ok := cfgs[0].(oasis.StdioMCPConfig)
	if !ok {
		t.Fatalf("expected StdioMCPConfig, got %T", cfgs[0])
	}
	if s.Name != "fs" || s.Command != "npx" || len(s.Args) != 2 {
		t.Errorf("got %+v", s)
	}
	if s.Env["FS_RO"] != "1" {
		t.Errorf("env: %+v", s.Env)
	}
}

func TestLoad_HTTPServer_BearerEnvVar(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{
        "version": 1,
        "mcpServers": {
            "github": {
                "url": "https://mcp.github.com/v1",
                "headers": {"X-API-Version": "2022"},
                "auth": {"type": "bearer", "envVar": "GH_TOKEN"},
                "timeout": "30s"
            }
        }
    }`)
	cfgs, err := Load(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("expected 1, got %d", len(cfgs))
	}
	h, ok := cfgs[0].(oasis.HTTPMCPConfig)
	if !ok {
		t.Fatalf("type: %T", cfgs[0])
	}
	if h.URL != "https://mcp.github.com/v1" {
		t.Errorf("url: %s", h.URL)
	}
	if h.Headers["X-API-Version"] != "2022" {
		t.Errorf("headers: %+v", h.Headers)
	}
	auth, ok := h.Auth.(oasis.BearerAuth)
	if !ok {
		t.Fatalf("auth type: %T", h.Auth)
	}
	if auth.EnvVar != "GH_TOKEN" {
		t.Errorf("env var: %s", auth.EnvVar)
	}
}

func TestLoad_EnvVarInterpolationInHeaders(t *testing.T) {
	t.Setenv("MY_HEADER", "interpolated-value")
	dir := t.TempDir()
	writeConfig(t, dir, `{
        "version": 1,
        "mcpServers": {
            "x": {
                "url": "https://x",
                "headers": {"X-Custom": "${MY_HEADER}"}
            }
        }
    }`)
	cfgs, _ := Load(dir)
	h := cfgs[0].(oasis.HTTPMCPConfig)
	if h.Headers["X-Custom"] != "interpolated-value" {
		t.Errorf("not interpolated: %s", h.Headers["X-Custom"])
	}
}

func TestLoad_MissingEnvVarErrors(t *testing.T) {
	os.Unsetenv("ABSENT_VAR_XYZ")
	dir := t.TempDir()
	writeConfig(t, dir, `{
        "version": 1,
        "mcpServers": {
            "x": {
                "url": "https://x",
                "headers": {"X-Missing": "${ABSENT_VAR_XYZ}"}
            }
        }
    }`)
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
}

func TestLoad_DiscriminatorConflict(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{
        "version": 1,
        "mcpServers": {
            "bad": {"command": "x", "url": "y"}
        }
    }`)
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error for both command+url")
	}
}

func TestLoad_WalkUp(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "a", "b", "c")
	os.MkdirAll(sub, 0755)
	writeConfig(t, dir, `{"version":1,"mcpServers":{"x":{"command":"echo"}}}`)
	cfgs, err := Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 1 {
		t.Errorf("expected to find via walk-up, got %d", len(cfgs))
	}
}

func TestLoad_FilterAndAlias(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{
        "version": 1,
        "mcpServers": {
            "github": {
                "url": "https://x",
                "filter": {"include": ["create_*"]},
                "aliases": {"create_issue": "gh_new_issue"}
            }
        }
    }`)
	cfgs, _ := Load(dir)
	h := cfgs[0].(oasis.HTTPMCPConfig)
	if h.Filter == nil || len(h.Filter.Include) != 1 {
		t.Errorf("filter: %+v", h.Filter)
	}
	if h.Aliases["create_issue"] != "gh_new_issue" {
		t.Errorf("aliases: %+v", h.Aliases)
	}
}

func TestLoad_Disabled(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{
        "version": 1,
        "mcpServers": {
            "x": {"command": "echo", "disabled": true}
        }
    }`)
	cfgs, _ := Load(dir)
	s := cfgs[0].(oasis.StdioMCPConfig)
	if !s.Disabled {
		t.Error("disabled not parsed")
	}
}
