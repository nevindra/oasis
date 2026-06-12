package core

import (
	"context"
	"encoding/json"
	"testing"
)

func TestUIComponent_RoundTrip(t *testing.T) {
	c := UIComponent{Name: "FlightCard", Props: json.RawMessage(`{"count":2}`)}
	r := ToolResult{Content: "x", UI: &c}
	if r.UI.Name != "FlightCard" {
		t.Fatalf("UI.Name = %q, want FlightCard", r.UI.Name)
	}
	if string(r.UI.Props) != `{"count":2}` {
		t.Fatalf("UI.Props = %s", r.UI.Props)
	}
}

func TestEventUIComponent_InAllStreamEventTypes(t *testing.T) {
	found := false
	for _, e := range AllStreamEventTypes() {
		if e == EventUIComponent {
			found = true
		}
	}
	if !found {
		t.Fatal("EventUIComponent missing from AllStreamEventTypes()")
	}
}

type uiOut struct {
	V int `json:"v"`
}

func (uiOut) UIComponent() string { return "Widget" }

type plainOut struct {
	V int `json:"v"`
}

type uiTool struct{}

func (uiTool) Definition() ToolMeta { return ToolMeta{Name: "ui", Description: "d"} }
func (uiTool) Execute(_ context.Context, _ struct{}) (uiOut, error) {
	return uiOut{V: 7}, nil
}

type plainTool struct{}

func (plainTool) Definition() ToolMeta { return ToolMeta{Name: "plain", Description: "d"} }
func (plainTool) Execute(_ context.Context, _ struct{}) (plainOut, error) {
	return plainOut{V: 7}, nil
}

func TestErase_SetsUIWhenOutRenderable(t *testing.T) {
	at := Erase[struct{}, uiOut](uiTool{})
	res, err := at.ExecuteRaw(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	if res.UI == nil {
		t.Fatal("UI is nil, want set")
	}
	if res.UI.Name != "Widget" {
		t.Fatalf("UI.Name = %q, want Widget", res.UI.Name)
	}
	if string(res.UI.Props) != `{"v":7}` {
		t.Fatalf("UI.Props = %s", res.UI.Props)
	}
	if res.Content != `{"v":7}` {
		t.Fatalf("Content = %q", res.Content)
	}
}

func TestErase_NoUIWhenOutNotRenderable(t *testing.T) {
	at := Erase[struct{}, plainOut](plainTool{})
	res, err := at.ExecuteRaw(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	if res.UI != nil {
		t.Fatalf("UI = %+v, want nil", res.UI)
	}
}

type uiStreamTool struct{ uiTool }

func (uiStreamTool) ExecuteStream(_ context.Context, _ struct{}, _ chan<- StreamEvent) (uiOut, error) {
	return uiOut{V: 7}, nil
}

func TestEraseStreaming_SetsUIWhenOutRenderable(t *testing.T) {
	st := EraseStreaming[struct{}, uiOut](uiStreamTool{})
	res, err := st.ExecuteStream(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	if res.UI == nil || res.UI.Name != "Widget" {
		t.Fatalf("UI = %+v, want Widget", res.UI)
	}
}

func TestFunc_SetsUIWhenOutRenderable(t *testing.T) {
	at := Func("ui", "d", func(_ context.Context, _ struct{}) (uiOut, error) {
		return uiOut{V: 7}, nil
	})
	res, err := at.ExecuteRaw(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	if res.UI == nil {
		t.Fatal("UI is nil, want set")
	}
	if res.UI.Name != "Widget" {
		t.Fatalf("UI.Name = %q, want Widget", res.UI.Name)
	}
	if string(res.UI.Props) != `{"v":7}` {
		t.Fatalf("UI.Props = %s", res.UI.Props)
	}
	if res.Content != `{"v":7}` {
		t.Fatalf("Content = %q", res.Content)
	}
}

func TestFunc_NoUIWhenOutNotRenderable(t *testing.T) {
	at := Func("plain", "d", func(_ context.Context, _ struct{}) (plainOut, error) {
		return plainOut{V: 7}, nil
	})
	res, err := at.ExecuteRaw(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	if res.UI != nil {
		t.Fatalf("UI = %+v, want nil", res.UI)
	}
}

func TestUIResult(t *testing.T) {
	type props struct {
		Title string `json:"title"`
	}
	r := UIResult("Card", props{Title: "hi"})
	if r.UI == nil {
		t.Fatal("UI is nil")
	}
	if r.UI.Name != "Card" {
		t.Fatalf("UI.Name = %q, want Card", r.UI.Name)
	}
	if string(r.UI.Props) != `{"title":"hi"}` {
		t.Fatalf("UI.Props = %s", r.UI.Props)
	}
	// Content mirrors the props JSON so the LLM still "sees" the rendered data.
	if r.Content != `{"title":"hi"}` {
		t.Fatalf("Content = %q", r.Content)
	}
	if r.Error != "" {
		t.Fatalf("Error = %q, want empty", r.Error)
	}
}
