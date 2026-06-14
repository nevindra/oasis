package mcp

import (
	"encoding/json"
	"testing"
)

func TestCallToolResult_Unmarshal(t *testing.T) {
	data := []byte(`{"content":[{"type":"text","text":"hi"}],"isError":false}`)
	var r CallToolResult
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(r.Content) != 1 || r.Content[0].Text != "hi" {
		t.Errorf("got %+v", r)
	}
	if r.IsError {
		t.Errorf("IsError should be false")
	}
}

func TestInitializeResult_Unmarshal(t *testing.T) {
	data := []byte(`{"protocolVersion":"2024-11-05","capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"test","version":"1.0"}}`)
	var r InitializeResult
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.ServerInfo.Name != "test" || r.ProtocolVersion != "2024-11-05" {
		t.Errorf("got %+v", r)
	}
	if r.Capabilities.Tools == nil || !r.Capabilities.Tools.ListChanged {
		t.Errorf("tools capability not decoded: %+v", r.Capabilities)
	}
}

// TestServerCapabilities_RoundTrip verifies that ServerCapabilities preserves
// the MCP JSON wire shape across marshal/unmarshal cycles.
func TestServerCapabilities_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		wire string
		want ServerCapabilities
	}{
		{
			name: "all capabilities",
			wire: `{"tools":{"listChanged":true},"resources":{},"prompts":{},"logging":{}}`,
			want: ServerCapabilities{
				Tools:     &CapabilityFlag{ListChanged: true},
				Resources: &CapabilityFlag{},
				Prompts:   &CapabilityFlag{},
				Logging:   &CapabilityFlag{},
			},
		},
		{
			name: "tools only",
			wire: `{"tools":{}}`,
			want: ServerCapabilities{
				Tools: &CapabilityFlag{},
			},
		},
		{
			name: "empty object",
			wire: `{}`,
			want: ServerCapabilities{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Unmarshal wire → struct.
			var got ServerCapabilities
			if err := json.Unmarshal([]byte(tc.wire), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			// Structural check.
			toolsMatch := (got.Tools == nil) == (tc.want.Tools == nil)
			if got.Tools != nil && tc.want.Tools != nil {
				toolsMatch = got.Tools.ListChanged == tc.want.Tools.ListChanged
			}
			if !toolsMatch {
				t.Errorf("tools mismatch: got %+v want %+v", got.Tools, tc.want.Tools)
			}
			if (got.Resources == nil) != (tc.want.Resources == nil) {
				t.Errorf("resources mismatch: got %v want %v", got.Resources, tc.want.Resources)
			}
			if (got.Prompts == nil) != (tc.want.Prompts == nil) {
				t.Errorf("prompts mismatch: got %v want %v", got.Prompts, tc.want.Prompts)
			}
			if (got.Logging == nil) != (tc.want.Logging == nil) {
				t.Errorf("logging mismatch: got %v want %v", got.Logging, tc.want.Logging)
			}

			// Marshal struct → JSON, then unmarshal back: round-trip identity.
			out, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got2 ServerCapabilities
			if err := json.Unmarshal(out, &got2); err != nil {
				t.Fatalf("second unmarshal: %v", err)
			}
			// Verify tools field survives the second trip.
			if (got2.Tools == nil) != (got.Tools == nil) {
				t.Errorf("round-trip tools mismatch: first=%v second=%v", got.Tools, got2.Tools)
			}
		})
	}
}

// TestServerCapabilities_OmitEmpty verifies absent fields are not emitted on marshal.
func TestServerCapabilities_OmitEmpty(t *testing.T) {
	caps := ServerCapabilities{Tools: &CapabilityFlag{}}
	out, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal map: %v", err)
	}
	if _, ok := m["resources"]; ok {
		t.Errorf("resources should be absent in JSON when nil, got %s", out)
	}
	if _, ok := m["tools"]; !ok {
		t.Errorf("tools should be present in JSON, got %s", out)
	}
}
