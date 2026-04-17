package mcpconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/nevindra/oasis"
)

const configFileName = "mcp.json"
const configDirName = ".oasis"

// Load discovers the nearest .oasis/mcp.json walking up from startDir,
// parses it, applies ${ENV_VAR} interpolation, and returns server configs.
// Missing file = empty result + nil error (soft default).
func Load(startDir string) ([]oasis.MCPServerConfig, error) {
	path, ok := findConfigFile(startDir)
	if !ok {
		return nil, nil
	}
	return LoadFile(path)
}

// LoadFile parses a specific config file path.
func LoadFile(path string) ([]oasis.MCPServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var schema fileSchema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if schema.Version != 1 {
		return nil, fmt.Errorf("unsupported config version %d (expected 1)", schema.Version)
	}

	out := make([]oasis.MCPServerConfig, 0, len(schema.MCPServers))
	for name, raw := range schema.MCPServers {
		cfg, err := parseServer(name, raw)
		if err != nil {
			return nil, fmt.Errorf("server %q: %w", name, err)
		}
		out = append(out, cfg)
	}
	return out, nil
}

// Discover returns candidate config file paths found by walking up from
// startDir, without parsing them. Returns nil if no config is found.
func Discover(startDir string) []string {
	if path, ok := findConfigFile(startDir); ok {
		return []string{path}
	}
	return nil
}

func findConfigFile(startDir string) (string, bool) {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return "", false
	}
	dir := abs
	for {
		candidate := filepath.Join(dir, configDirName, configFileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func parseServer(name string, raw json.RawMessage) (oasis.MCPServerConfig, error) {
	var r rawServer
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}

	hasCmd := r.Command != ""
	hasURL := r.URL != ""
	if hasCmd && hasURL {
		return nil, errors.New(`both "command" and "url" set — pick one transport`)
	}
	if !hasCmd && !hasURL {
		return nil, errors.New(`neither "command" nor "url" set`)
	}

	// Interpolate env vars in headers (and other string fields if needed).
	headers, err := interpolateMap(r.Headers)
	if err != nil {
		return nil, fmt.Errorf("headers: %w", err)
	}

	var filter *oasis.MCPToolFilter
	if r.Filter != nil {
		filter = &oasis.MCPToolFilter{Include: r.Filter.Include, Exclude: r.Filter.Exclude}
	}

	if hasCmd {
		return oasis.StdioMCPConfig{
			Name:     name,
			Command:  r.Command,
			Args:     r.Args,
			Env:      r.Env,
			WorkDir:  r.WorkDir,
			Disabled: r.Disabled,
			Filter:   filter,
			Aliases:  r.Aliases,
		}, nil
	}

	var auth oasis.Auth
	if r.Auth != nil {
		switch r.Auth.Type {
		case "bearer":
			auth = oasis.BearerAuth{Token: r.Auth.Token, EnvVar: r.Auth.EnvVar}
		default:
			return nil, fmt.Errorf("unsupported auth type %q (only \"bearer\" supported in v1)", r.Auth.Type)
		}
	}

	timeout := 30 * time.Second
	if r.Timeout != "" {
		d, err := time.ParseDuration(r.Timeout)
		if err != nil {
			return nil, fmt.Errorf("timeout: %w", err)
		}
		timeout = d
	}

	return oasis.HTTPMCPConfig{
		Name:     name,
		URL:      r.URL,
		Headers:  headers,
		Auth:     auth,
		Timeout:  timeout,
		Disabled: r.Disabled,
		Filter:   filter,
		Aliases:  r.Aliases,
	}, nil
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func interpolateMap(m map[string]string) (map[string]string, error) {
	if m == nil {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		result, err := interpolateString(v)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		out[k] = result
	}
	return out, nil
}

func interpolateString(s string) (string, error) {
	var firstErr error
	result := envPattern.ReplaceAllStringFunc(s, func(match string) string {
		varName := match[2 : len(match)-1]
		val, ok := os.LookupEnv(varName)
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("env var %s is unset", varName)
			}
			return match
		}
		return val
	})
	return result, firstErr
}
