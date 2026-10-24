package kiro

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesFromKiro(t *testing.T) {
	req := KiroRequest{
		Instructions: "follow",
		ProfileArn:   "arn:p",
		ConversationState: ConversationState{
			ConversationID:      "c1",
			AgentContinuationID: "c1",
			AgentTaskType:       "vibe",
			ChatTriggerType:     "MANUAL",
			CurrentMessage: CurrentMessage{UserInputMessage: UserInputMessage{
				Content: "hi",
				ModelID: "claude-opus-5.5",
				Images:  []KiroImage{{Format: "PNG", Source: KiroImageSource{Bytes: "YQ=="}}},
				UserInputMessageContext: UserInputMessageContext{
					Tools: []KiroTool{{ToolSpecification: ToolSpecification{
						Name: "Read", Description: "r", InputSchema: InputSchema{JSON: json.RawMessage(`{"type":"object"}`)},
					}}},
					ToolResults: []ToolResult{{ToolUseID: "t0", Content: []map[string]any{{"text": "ok"}}}},
				},
			}},
			History: []HistoryMessage{{Assistant: &AssistantMessage{
				Content:  "was",
				ToolUses: []ToolUseEntry{{ToolUseID: "t0", Name: "Read", Input: json.RawMessage(`{"p":1}`)}},
			}}},
		},
	}
	raw, err := MarshalNoEscape(req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ResponsesFromKiro(string(raw), "vibe", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "claude-opus-5.5" || got["origin"] != "AI_EDITOR" || got["stream"] != true || got["instructions"] != "follow" || got["profileArn"] != "arn:p" {
		t.Fatalf("%s", out)
	}
	if strings.Contains(out, "chatTriggerType") || strings.Contains(out, "agent_task_type") || strings.Contains(out, "modelId") {
		t.Fatalf("old fields leaked: %s", out)
	}
	meta := got["metadata"].(map[string]any)
	if meta["kiro:agent_mode"] != "vibe" || meta["kiro:conversation_id"] != "c1" || meta["kiro:root_conversation_id"] != "c1" {
		t.Fatalf("%v", meta)
	}
	in := got["input"].([]any)
	if in[0].(map[string]any)["role"] != "assistant" || in[1].(map[string]any)["type"] != "function_call" {
		t.Fatalf("history order %s", out)
	}
	if !strings.Contains(out, "data:image/png;base64,YQ==") || strings.Contains(out, "WVE9PQ") {
		t.Fatalf("image encoding %s", out)
	}
	tools := got["tools"].([]any)
	if tools[0].(map[string]any)["name"] != "Read" {
		t.Fatal(out)
	}
}

func TestSSEToolAndUsage(t *testing.T) {
	var d SSEDecoder
	chunk := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"Hi"}`,
		`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"t1","name":"Read","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","delta":"{\"p\":"}`,
		`data: {"type":"response.function_call_arguments.delta","delta":"1}"}`,
		`data: {"type":"response.function_call_arguments.done"}`,
		`data: {"type":"response.completed","response":{"usage":{"kiro:context_usage_percentage":0.5}}}`,
		"",
	}, "\n")
	if err := d.Feed([]byte(chunk[:20])); err != nil {
		t.Fatal(err)
	}
	if err := d.Feed([]byte(chunk[20:])); err != nil {
		t.Fatal(err)
	}
	var text, args string
	var stops int
	var pct float64
	for {
		ev, err := d.NextEvent()
		if err != nil {
			t.Fatal(err)
		}
		if ev == nil {
			break
		}
		switch ev.Kind {
		case EvAssistant:
			text += ev.Content
		case EvToolUse:
			args += ev.ToolInput
			if ev.ToolStop {
				stops++
			}
			if ev.ToolName != "" && ev.ToolName != "Read" {
				t.Fatal(ev.ToolName)
			}
		case EvContextUsage:
			pct = ev.ContextUsage
		}
	}
	if text != "Hi" || args != `{"p":1}` || stops != 1 || pct != 0.5 {
		t.Fatalf("text=%q args=%q stops=%d pct=%v", text, args, stops, pct)
	}
}
