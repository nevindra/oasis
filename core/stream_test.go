package core

import (
	"encoding/json"
	"testing"
)

func TestFinishReasonValues(t *testing.T) {
	cases := []struct {
		got  FinishReason
		want string
	}{
		{FinishStop, "stop"},
		{FinishToolCalls, "tool-calls"},
		{FinishLength, "length"},
		{FinishContentFilter, "content-filter"},
		{FinishHalted, "halted"},
		{FinishSuspended, "suspended"},
		{FinishMaxIter, "max-iterations"},
		{FinishError, "error"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("FinishReason %q != %q", c.got, c.want)
		}
	}
}

func TestNewEventTypeValues(t *testing.T) {
	cases := []struct {
		got  StreamEventType
		want string
	}{
		{EventRunStart, "run-start"},
		{EventRunFinish, "run-finish"},
		{EventIterationStart, "iteration-start"},
		{EventIterationFinish, "iteration-finish"},
		{EventObjectDelta, "object-delta"},
		{EventObjectFinish, "object-finish"},
		{EventElementDelta, "element-delta"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("event type %q != %q", c.got, c.want)
		}
	}
}

func TestStreamEventNewFieldsRoundTrip(t *testing.T) {
	ev := StreamEvent{
		Type:         EventRunFinish,
		FinishReason: FinishStop,
		Warnings:     []string{"rate-limited"},
		ProviderMeta: []byte(`{"stop_sequence":"END"}`),
		Object:       []byte(`{"title":"x"}`),
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back StreamEvent
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.FinishReason != FinishStop {
		t.Errorf("FinishReason lost: %q", back.FinishReason)
	}
	if len(back.Warnings) != 1 || back.Warnings[0] != "rate-limited" {
		t.Errorf("Warnings lost: %#v", back.Warnings)
	}
	if string(back.ProviderMeta) != `{"stop_sequence":"END"}` {
		t.Errorf("ProviderMeta lost: %s", back.ProviderMeta)
	}
	if string(back.Object) != `{"title":"x"}` {
		t.Errorf("Object lost: %s", back.Object)
	}
}
