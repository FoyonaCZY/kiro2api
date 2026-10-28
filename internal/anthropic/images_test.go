package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/FoyonaCZY/kiro2api/internal/kiro"
)

func imageTurns(t *testing.T, turns, perTurn, current int) *MessagesRequest {
	t.Helper()
	img := map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "AAAA"}}
	blocks := func(n int, text string) []any {
		out := []any{map[string]any{"type": "text", "text": text}}
		for i := 0; i < n; i++ {
			out = append(out, img)
		}
		return out
	}
	var msgs []any
	for i := 0; i < turns; i++ {
		msgs = append(msgs, map[string]any{"role": "user", "content": blocks(perTurn, "turn")}, map[string]any{"role": "assistant", "content": "ok"})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": blocks(current, "now")})
	b, _ := json.Marshal(map[string]any{"model": "claude-opus-5-5", "max_tokens": 10, "messages": msgs})
	return mustReq(t, string(b))
}

func countImages(res *ConversionResult) (history, current, placeholders int) {
	for _, h := range res.State.History {
		if h.User != nil {
			history += len(h.User.Images)
			placeholders += strings.Count(h.User.Content, "omitted from older conversation history")
		}
	}
	return history, len(res.State.CurrentMessage.UserInputMessage.Images), placeholders
}

func TestOldHistoryImagesAreOmitted(t *testing.T) {
	res, err := ConvertRequest(imageTurns(t, 8, 3, 1))
	if err != nil {
		t.Fatal(err)
	}
	h, c, p := countImages(res)
	// 当前 + 最近 4 条历史保留图片，更早的 4 条换成占位
	if c != 1 || h != 12 || p != 4 {
		t.Fatalf("history=%d current=%d placeholders=%d", h, c, p)
	}
	first := res.State.History[0].User.Content
	if !strings.HasPrefix(first, "turn\n[This message contained 3 image(s)") {
		t.Fatal(first)
	}
}

func TestImageTotalCappedAtTwenty(t *testing.T) {
	// 4 条历史各 5 张 + 当前 1 张 = 21，超过 20，最旧一条被去掉
	res, err := ConvertRequest(imageTurns(t, 4, 5, 1))
	if err != nil {
		t.Fatal(err)
	}
	h, c, p := countImages(res)
	if c != 1 || h+c > maxImagesPerRequest || h != 15 || p != 1 {
		t.Fatalf("history=%d current=%d placeholders=%d", h, c, p)
	}
	// 当前消息的图片永远不裁
	res, _ = ConvertRequest(imageTurns(t, 2, 5, 25))
	h, c, _ = countImages(res)
	if c != 25 || h != 0 {
		t.Fatalf("history=%d current=%d", h, c)
	}
	// 图片不多时什么都不动
	res, _ = ConvertRequest(imageTurns(t, 2, 2, 1))
	if h, c, p := countImages(res); h != 4 || c != 1 || p != 0 {
		t.Fatalf("history=%d current=%d placeholders=%d", h, c, p)
	}
}

// readImageRequest Claude Code 的 Read 读图片：tool_result 的 content 里只有 image 块。
func readImageRequest(t *testing.T, result string) *MessagesRequest {
	t.Helper()
	return mustReq(t, `{"model":"claude-opus-5-5","max_tokens":10,"metadata":{"user_id":"u_session_0b4445e1-f5be-49e1-87ce-62bbc28ad705"},"messages":[
		{"role":"user","content":"看看 a.png 和 b.png"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.png"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUFB"}}]}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"b.png"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":`+result+`}]}]}`)
}

func TestToolResultImagesReachUpstream(t *testing.T) {
	res, err := ConvertRequest(readImageRequest(t, `[{"type":"text","text":"b.png"},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"QkJC"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	cur := res.State.CurrentMessage.UserInputMessage
	if len(cur.Images) != 1 || cur.Images[0].Format != "jpeg" || cur.Images[0].Source.Bytes != "QkJC" {
		t.Fatalf("current images %+v", cur.Images)
	}
	if got := resultText(cur.UserInputMessageContext.ToolResults); !strings.HasPrefix(got, "b.png\n[1 image(s)") {
		t.Fatalf("current tool result %q", got)
	}
	var hist *kiro.UserMessage
	for _, h := range res.State.History {
		if h.User != nil && h.User.UserInputMessageContext != nil && len(h.User.UserInputMessageContext.ToolResults) > 0 {
			hist = h.User
		}
	}
	if hist == nil || len(hist.Images) != 1 || hist.Images[0].Source.Bytes != "QUFB" {
		t.Fatalf("history tool result images %+v", hist)
	}

	body, err := json.Marshal(KiroBody(res))
	if err != nil {
		t.Fatal(err)
	}
	out, err := kiro.ResponsesFromKiro(string(body), "vibe", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"data:image/png;base64,QUFB", "data:image/jpeg;base64,QkJC", `"call_id":"t2"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
}

func TestToolResultImagesDoNotStartNewTurn(t *testing.T) {
	withImage, err := ConvertRequest(readImageRequest(t, `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QkJC"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	textOnly, err := ConvertRequest(readImageRequest(t, `"b"`))
	if err != nil {
		t.Fatal(err)
	}
	if withImage.State.AgentContinuationID != textOnly.State.AgentContinuationID || withImage.CallPosition != textOnly.CallPosition {
		t.Fatalf("turn changed: %s/%d vs %s/%d", withImage.State.AgentContinuationID, withImage.CallPosition,
			textOnly.State.AgentContinuationID, textOnly.CallPosition)
	}
}

// TestAgentLoopKeepsUserImage 用户贴图后 Agent 连续调 8 次工具，只带工具结果的消息不算新一轮，图片要一直带着。
func TestAgentLoopKeepsUserImage(t *testing.T) {
	img := map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "AAAA"}}
	msgs := []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "看图"}, img}}}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("t%d", i)
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": "Read", "input": map[string]any{}}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "ok"}}})
	}
	b, _ := json.Marshal(map[string]any{"model": "claude-opus-5-5", "max_tokens": 10, "messages": msgs})
	res, err := ConvertRequest(mustReq(t, string(b)))
	if err != nil {
		t.Fatal(err)
	}
	if h, c, p := countImages(res); h != 1 || c != 0 || p != 0 {
		t.Fatalf("history=%d current=%d placeholders=%d", h, c, p)
	}

	// 之后用户又说了 5 轮话，这张图才算旧图。
	for i := 0; i < 5; i++ {
		msgs = append(msgs, map[string]any{"role": "assistant", "content": "ok"}, map[string]any{"role": "user", "content": "next"})
	}
	b, _ = json.Marshal(map[string]any{"model": "claude-opus-5-5", "max_tokens": 10, "messages": msgs})
	res, err = ConvertRequest(mustReq(t, string(b)))
	if err != nil {
		t.Fatal(err)
	}
	if h, _, p := countImages(res); h != 0 || p != 1 {
		t.Fatalf("history=%d placeholders=%d", h, p)
	}
}

// TestUnsupportedMediaPlaceholders URL 图片、Files API 图片和文档块发不出去，换成占位文字，不能悄悄消失。
func TestUnsupportedMediaPlaceholders(t *testing.T) {
	res, err := ConvertRequest(mustReq(t, `{"model":"claude-opus-5-5","max_tokens":10,"messages":[
		{"role":"user","content":"读一下"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Fetch","input":{}}]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"t1","content":[
				{"type":"text","text":"done"},
				{"type":"image","source":{"type":"url","url":"https://example.com/b.png"}},
				{"type":"document","title":"报告.pdf","source":{"type":"base64","media_type":"application/pdf","data":"JVBE"}}]},
			{"type":"text","text":"看这个"},
			{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}},
			{"type":"image","source":{"type":"file","file_id":"file_1"}},
			{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBE"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	cur := res.State.CurrentMessage.UserInputMessage
	if len(cur.Images) != 0 {
		t.Fatalf("images %+v", cur.Images)
	}
	want := strings.Join([]string{
		"看这个",
		"[Image omitted: this image could not be passed to the model (type=url, url=https://example.com/a.png).]",
		"[Image omitted: this image could not be passed to the model (type=file).]",
		"[Document omitted: document attachments could not be passed to the model.]",
	}, "\n")
	if cur.Content != want {
		t.Fatalf("content %q", cur.Content)
	}
	wantResult := strings.Join([]string{
		"done",
		"[Image omitted: this image could not be passed to the model (type=url, url=https://example.com/b.png).]",
		`[Document omitted: "报告.pdf" could not be passed to the model.]`,
	}, "\n")
	if got := resultText(cur.UserInputMessageContext.ToolResults); got != wantResult {
		t.Fatalf("tool result %q", got)
	}

	// 只有一份文档的用户消息也是用户开了新一轮。
	if !hasUserInput(json.RawMessage(`[{"type":"document","source":{"type":"url","url":"https://example.com/a.pdf"}}]`)) {
		t.Fatal("document should start a turn")
	}
}

func resultText(results []kiro.ToolResult) string {
	var parts []string
	for _, r := range results {
		for _, c := range r.Content {
			s, _ := c["text"].(string)
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}
