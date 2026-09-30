package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FoyonaCZY/kiro2api/internal/kiro"
	"github.com/google/uuid"
)

const searchPrefix = "Perform a web search for the query: "

// HasWebSearchTool 只有一个工具且名为 web_search。
func HasWebSearchTool(r *MessagesRequest) bool {
	return len(r.Tools) == 1 && r.Tools[0].Name == "web_search"
}

// ExtractSearchQuery 第一条消息第一个 text 块，去掉固定前缀。
func ExtractSearchQuery(r *MessagesRequest) string {
	if len(r.Messages) == 0 {
		return ""
	}
	s, isStr, blocks := ParseBlocks(r.Messages[0].Content)
	text := s
	if !isStr {
		if len(blocks) == 0 || blocks[0].Type != "text" || blocks[0].Text == nil {
			return ""
		}
		text = *blocks[0].Text
	}
	return strings.TrimPrefix(text, searchPrefix)
}

func randString(n int, charset string) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = charset[rand.IntN(len(charset))]
	}
	return string(b)
}

type mcpRequest struct {
	ID      string `json:"id"`
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  struct {
		Name      string `json:"name"`
		Arguments struct {
			Query string `json:"query"`
		} `json:"arguments"`
	} `json:"params"`
}

func newMCPRequest(query string) (string, mcpRequest) {
	var r mcpRequest
	r.ID = fmt.Sprintf("web_search_tooluse_%s_%d_%s",
		randString(22, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"),
		time.Now().UnixMilli(), randString(8, "abcdefghijklmnopqrstuvwxyz0123456789"))
	r.JSONRPC, r.Method = "2.0", "tools/call"
	r.Params.Name = "web_search"
	r.Params.Arguments.Query = query
	return "srvtoolu_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:32], r
}

type searchResult struct {
	Title         string  `json:"title"`
	URL           string  `json:"url"`
	Snippet       *string `json:"snippet"`
	PublishedDate *int64  `json:"publishedDate"`
}

type searchResults struct {
	Results []searchResult `json:"results"`
}

func callSearch(ctx context.Context, p *kiro.Provider, req mcpRequest) (*searchResults, error) {
	body, _ := Marshal(req)
	resp, err := p.CallMCP(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var mr struct {
		Error *struct {
			Code    *int    `json:"code"`
			Message *string `json:"message"`
		} `json:"error"`
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &mr); err != nil {
		return nil, err
	}
	if mr.Error != nil {
		code, msg := -1, "Unknown error"
		if mr.Error.Code != nil {
			code = *mr.Error.Code
		}
		if mr.Error.Message != nil {
			msg = *mr.Error.Message
		}
		return nil, fmt.Errorf("MCP error: %d - %s", code, msg)
	}
	if mr.Result == nil || len(mr.Result.Content) == 0 || mr.Result.Content[0].Type != "text" {
		return nil, nil
	}
	var sr searchResults
	if json.Unmarshal([]byte(mr.Result.Content[0].Text), &sr) != nil {
		return nil, nil
	}
	return &sr, nil
}

func searchSummary(query string, r *searchResults) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Here are the search results for \"%s\":\n\n", query)
	if r != nil {
		for i, x := range r.Results {
			fmt.Fprintf(&b, "%d. **%s**\n", i+1, x.Title)
			if x.Snippet != nil {
				s := *x.Snippet
				if utf8.RuneCountInString(s) > 200 {
					s = string([]rune(s)[:200]) + "..."
				}
				fmt.Fprintf(&b, "   %s\n", s)
			}
			fmt.Fprintf(&b, "   Source: %s\n\n", x.URL)
		}
	} else {
		b.WriteString("No results found.\n")
	}
	b.WriteString("\nPlease note that these are web search results and may not be fully accurate or up-to-date.")
	return b.String()
}

// WebSearchEvents 按官方格式拼出 server_tool_use + web_search_tool_result + 摘要文本。
func WebSearchEvents(model, query, toolUseID string, r *searchResults, inputTokens int) []SSEEvent {
	msgID := "msg_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	stop := func(i int) SSEEvent { return stopEvent(i) }
	evs := []SSEEvent{
		{"message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": msgID, "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil,
			"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": 0, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
		}}},
		{"content_block_start", textStart(0)},
		{"content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": fmt.Sprintf("I'll search for \"%s\".", query)}}},
		stop(0),
		{"content_block_start", map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{
			"id": toolUseID, "type": "server_tool_use", "name": "web_search", "input": map[string]any{"query": query},
		}}},
		stop(1),
	}
	content := []any{}
	if r != nil {
		for _, x := range r.Results {
			var age any
			if x.PublishedDate != nil {
				age = time.UnixMilli(*x.PublishedDate).UTC().Format("January 2, 2006")
			}
			snippet := ""
			if x.Snippet != nil {
				snippet = *x.Snippet
			}
			content = append(content, map[string]any{"type": "web_search_result", "title": x.Title, "url": x.URL, "encrypted_content": snippet, "page_age": age})
		}
	}
	evs = append(evs,
		SSEEvent{"content_block_start", map[string]any{"type": "content_block_start", "index": 2, "content_block": map[string]any{"type": "web_search_tool_result", "content": content}}},
		stop(2),
		SSEEvent{"content_block_start", textStart(3)},
	)
	summary := searchSummary(query, r)
	runes := []rune(summary)
	for i := 0; i < len(runes); i += 100 {
		end := min(i+100, len(runes))
		evs = append(evs, SSEEvent{"content_block_delta", map[string]any{"type": "content_block_delta", "index": 3, "delta": map[string]any{"type": "text_delta", "text": string(runes[i:end])}}})
	}
	return append(evs, stop(3),
		SSEEvent{"message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"},
			"usage": map[string]any{"output_tokens": (len(summary) + 3) / 4, "server_tool_use": map[string]any{"web_search_requests": 1}}}},
		SSEEvent{"message_stop", map[string]any{"type": "message_stop"}},
	)
}

// RunWebSearch 调 MCP 搜索；限流冷却直接返回错误，其它失败按无结果处理。
func RunWebSearch(ctx context.Context, p *kiro.Provider, r *MessagesRequest, inputTokens int) ([]SSEEvent, error) {
	query := ExtractSearchQuery(r)
	if query == "" {
		return nil, errEmptyQuery
	}
	slog.Info("处理 WebSearch 请求", "query", query)
	id, req := newMCPRequest(query)
	res, err := callSearch(ctx, p, req)
	if err != nil {
		var cd *kiro.CoolingDownError
		if errors.As(err, &cd) {
			return nil, err
		}
		slog.Warn("MCP API 调用失败", "err", err)
		res = nil
	}
	return WebSearchEvents(r.Model, query, id, res, inputTokens), nil
}

var errEmptyQuery = errors.New("无法从消息中提取搜索查询")
