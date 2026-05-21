package core

import (
	"encoding/json"
	"testing"
)

func TestChatResponseNewFieldsRoundTrip(t *testing.T) {
	resp := ChatResponse{
		Content:      "hi",
		FinishReason: FinishStop,
		Warnings:     []string{"fallback-model"},
		ProviderMeta: []byte(`{"x":1}`),
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ChatResponse
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.FinishReason != FinishStop {
		t.Errorf("FinishReason lost: %q", back.FinishReason)
	}
	if len(back.Warnings) != 1 {
		t.Errorf("Warnings lost")
	}
	if string(back.ProviderMeta) != `{"x":1}` {
		t.Errorf("ProviderMeta lost")
	}
}
