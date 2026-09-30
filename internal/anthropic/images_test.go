package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
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
