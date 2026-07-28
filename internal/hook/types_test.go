package hook

import "testing"

func TestRequiredLifecycleEventsAreSupported(t *testing.T) {
	required := []Event{
		EventPreToolUse,
		EventPostToolUse,
		EventSessionStart,
		EventUserPromptSubmit,
		EventStop,
		EventPreCompact,
		EventPostCompact,
		EventSubagentStart,
		EventSubagentStop,
	}
	available := make(map[Event]bool, len(AllEvents))
	for _, event := range AllEvents {
		if !event.Valid() {
			t.Fatalf("AllEvents contains invalid event %q", event)
		}
		available[event] = true
		parsed, err := ParseEvent(event.String())
		if err != nil || parsed != event {
			t.Fatalf("ParseEvent(%q) = %q, %v", event, parsed, err)
		}
	}
	for _, event := range required {
		if !available[event] {
			t.Errorf("required event %q is missing from AllEvents", event)
		}
	}
	if len(available) != len(required) {
		t.Fatalf("supported event count = %d, want %d", len(available), len(required))
	}
}
