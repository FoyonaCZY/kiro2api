package usage

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type note struct {
	index int
	score int
	line  string
}

type hit struct {
	path string
	n    int
	note string
}

const currentIndex = int(^uint(0) >> 1)

// PayloadProfile 出站 Kiro 请求的结构摘要，只记路径、长度和图片尺寸，方便对照「Input is too long」。
func PayloadProfile(body string) string {
	var v any
	if json.Unmarshal([]byte(body), &v) != nil {
		return fmt.Sprintf("bytes=%d json=invalid", len(body))
	}
	root, _ := v.(map[string]any)
	if input, ok := root["input"].([]any); ok {
		return responsesProfile(body, v, root, input)
	}
	state := root
	if s, ok := root["conversationState"].(map[string]any); ok {
		state = s
	}
	var images, imageBytes, tools, toolUses, toolResults int
	var notable []note
	history, _ := state["history"].([]any)
	for i, m := range history {
		notable = append(notable, messageProfile(i, m, &images, &imageBytes, &tools, &toolUses, &toolResults))
	}
	if cur, ok := state["currentMessage"]; ok {
		notable = append(notable, messageProfile(currentIndex, cur, &images, &imageBytes, &tools, &toolUses, &toolResults))
	}
	var hits []hit
	collectLarge(v, "", &hits)
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].n > hits[j].n })
	if len(hits) > 8 {
		hits = hits[:8]
	}
	kept := notable[:0]
	for _, n := range notable {
		if n.score >= 2000 {
			kept = append(kept, n)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].score > kept[j].score })
	if len(kept) > 24 {
		kept = kept[:24]
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].index < kept[j].index })
	lines := []string{fmt.Sprintf("bytes=%d history=%d images=%d image_bytes=%d tools=%d tool_uses=%d tool_results=%d",
		len(body), len(history), images, imageBytes, tools, toolUses, toolResults)}
	for _, h := range hits {
		lines = append(lines, fmt.Sprintf("largest %s %d%s", h.path, h.n, h.note))
	}
	for _, n := range kept {
		lines = append(lines, n.line)
	}
	return strings.Join(lines, "\n")
}

// responsesProfile /v1/responses 的 body：input 是 OpenAI Responses 风格的条目列表。
func responsesProfile(body string, v any, root map[string]any, input []any) string {
	tools, _ := root["tools"].([]any)
	var images, imageBytes, calls, outputs, reasoning, msgs int
	var notable []note
	for i, it := range input {
		m, _ := it.(map[string]any)
		label := fmt.Sprintf("i%d", i)
		switch m["type"] {
		case "function_call":
			calls++
			n := strLen(m, "arguments")
			notable = append(notable, note{i, n, fmt.Sprintf("%s function_call %v args=%d", label, m["name"], n)})
		case "function_call_output":
			outputs++
			n := strLen(m, "output")
			notable = append(notable, note{i, n, fmt.Sprintf("%s function_call_output %d", label, n)})
		case "reasoning":
			reasoning++
			n := strLen(m, "encrypted_content")
			notable = append(notable, note{i, n, fmt.Sprintf("%s reasoning %d", label, n)})
		default:
			msgs++
			role, _ := m["role"].(string)
			text, count, bytes := 0, 0, 0
			var dims []string
			switch c := m["content"].(type) {
			case string:
				text = len(c)
			case []any:
				for _, p := range c {
					pm, _ := p.(map[string]any)
					if pm["type"] == "input_image" {
						url, _ := pm["image_url"].(string)
						data := url[strings.IndexByte(url, ',')+1:]
						count++
						bytes += len(data)
						if kind, w, h, ok := imageMeta(data); ok {
							dims = append(dims, fmt.Sprintf("%s %dx%d", kind, w, h))
						}
					} else {
						text += strLen(pm, "text")
					}
				}
			}
			images += count
			imageBytes += bytes
			d := ""
			if len(dims) > 0 {
				d = " " + strings.Join(dims, ",")
			}
			notable = append(notable, note{i, text + bytes, fmt.Sprintf("%s %s content=%d images=%d/%d%s", label, role, text, count, bytes, d)})
		}
	}
	var hits []hit
	collectLarge(v, "", &hits)
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].n > hits[j].n })
	if len(hits) > 8 {
		hits = hits[:8]
	}
	kept := notable[:0]
	for _, n := range notable {
		if n.score >= 2000 {
			kept = append(kept, n)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].score > kept[j].score })
	if len(kept) > 24 {
		kept = kept[:24]
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].index < kept[j].index })
	lines := []string{fmt.Sprintf("bytes=%d input=%d messages=%d images=%d image_bytes=%d tools=%d function_calls=%d function_outputs=%d reasoning=%d",
		len(body), len(input), msgs, images, imageBytes, len(tools), calls, outputs, reasoning)}
	for _, h := range hits {
		lines = append(lines, fmt.Sprintf("largest %s %d%s", h.path, h.n, h.note))
	}
	for _, n := range kept {
		lines = append(lines, n.line)
	}
	return strings.Join(lines, "\n")
}

func strLen(v any, key string) int {
	m, _ := v.(map[string]any)
	s, _ := m[key].(string)
	return len(s)
}

func jsonLen(v any) int {
	b, _ := json.Marshal(v)
	return len(b)
}

func messageProfile(index int, msg any, images, imageBytes, tools, toolUses, toolResults *int) note {
	label := fmt.Sprintf("m%d", index)
	if index == currentIndex {
		label = "current"
	}
	m, _ := msg.(map[string]any)
	if user, ok := m["userInputMessage"].(map[string]any); ok {
		content := strLen(user, "content")
		count, bytes, dims := imageStats(user["images"])
		*images += count
		*imageBytes += bytes
		ctx, _ := user["userInputMessageContext"].(map[string]any)
		tl, _ := ctx["tools"].([]any)
		results, _ := ctx["toolResults"].([]any)
		rb := 0
		for _, r := range results {
			rm, _ := r.(map[string]any)
			n := strLen(rm, "content") + strLen(rm, "text")
			if c, ok := rm["content"]; ok {
				n = max(n, jsonLen(c))
			}
			rb += n
		}
		*tools += len(tl)
		*toolResults += len(results)
		return note{index, content + bytes + rb, fmt.Sprintf("%s user content=%d images=%d/%d%s tools=%d tool_results=%d/%d", label, content, count, bytes, dims, len(tl), len(results), rb)}
	}
	if a, ok := m["assistantResponseMessage"].(map[string]any); ok {
		content := strLen(a, "content")
		uses, _ := a["toolUses"].([]any)
		ub := 0
		for _, u := range uses {
			um, _ := u.(map[string]any)
			ub += jsonLen(um["input"])
		}
		*toolUses += len(uses)
		return note{index, content + ub, fmt.Sprintf("%s assistant content=%d tool_uses=%d/%d", label, content, len(uses), ub)}
	}
	return note{index, 0, label + " other"}
}

func imageStats(v any) (int, int, string) {
	items, ok := v.([]any)
	if !ok {
		return 0, 0, ""
	}
	bytes := 0
	var dims []string
	for _, it := range items {
		m, _ := it.(map[string]any)
		data := ""
		if src, ok := m["source"].(map[string]any); ok {
			data, _ = src["bytes"].(string)
		} else {
			data, _ = m["bytes"].(string)
		}
		bytes += len(data)
		if kind, w, h, ok := imageMeta(data); ok {
			dims = append(dims, fmt.Sprintf("%s %dx%d", kind, w, h))
		}
	}
	note := ""
	if len(dims) > 0 {
		note = " " + strings.Join(dims, ",")
	}
	return len(items), bytes, note
}

func collectLarge(v any, path string, hits *[]hit) {
	switch t := v.(type) {
	case map[string]any:
		for k, c := range t {
			next := k
			if path != "" {
				next = path + "." + k
			}
			collectLarge(c, next, hits)
		}
	case []any:
		for i, c := range t {
			collectLarge(c, fmt.Sprintf("%s[%d]", path, i), hits)
		}
	case string:
		if len(t) >= 256 {
			n := ""
			data := t
			if strings.HasSuffix(path, "image_url") {
				data = t[strings.IndexByte(t, ',')+1:]
			}
			if strings.HasSuffix(path, "bytes") || strings.HasSuffix(path, "image_url") {
				if kind, w, h, ok := imageMeta(data); ok {
					n = fmt.Sprintf(" %s %dx%d", kind, w, h)
				}
			}
			*hits = append(*hits, hit{path, len(t), n})
		}
	}
}

func b64val(c byte) (byte, bool) {
	switch {
	case c >= 'A' && c <= 'Z':
		return c - 'A', true
	case c >= 'a' && c <= 'z':
		return c - 'a' + 26, true
	case c >= '0' && c <= '9':
		return c - '0' + 52, true
	case c == '+' || c == '-':
		return 62, true
	case c == '/' || c == '_':
		return 63, true
	}
	return 0, false
}

func decodeB64Prefix(data string, maxBytes int) []byte {
	limit := (maxBytes + 2) / 3 * 4
	chars := make([]byte, 0, limit)
	for i := 0; i < len(data) && len(chars) < limit; i++ {
		if v, ok := b64val(data[i]); ok {
			chars = append(chars, v)
		}
	}
	if len(chars) < 4 {
		return nil
	}
	var out []byte
	for i := 0; i+4 <= len(chars) && len(out) < maxBytes; i += 4 {
		a, b, c, d := chars[i], chars[i+1], chars[i+2], chars[i+3]
		out = append(out, a<<2|b>>4, b<<4|c>>2, c<<6|d)
	}
	if len(out) > maxBytes {
		out = out[:maxBytes]
	}
	return out
}

func imageMeta(data string) (string, uint32, uint32, bool) {
	b := decodeB64Prefix(data, 128)
	if b == nil {
		return "", 0, 0, false
	}
	if len(b) >= 24 && string(b[:8]) == "\x89PNG\r\n\x1a\n" && string(b[12:16]) == "IHDR" {
		return "png", binary.BigEndian.Uint32(b[16:20]), binary.BigEndian.Uint32(b[20:24]), true
	}
	if len(b) >= 4 && b[0] == 0xFF && b[1] == 0xD8 {
		i := 2
		for i+9 < len(b) {
			if b[i] != 0xFF {
				break
			}
			if mk := b[i+1]; mk == 0xC0 || mk == 0xC1 || mk == 0xC2 {
				h := uint32(binary.BigEndian.Uint16(b[i+5:]))
				w := uint32(binary.BigEndian.Uint16(b[i+7:]))
				return "jpeg", w, h, true
			}
			n := int(binary.BigEndian.Uint16(b[i+2:]))
			if n < 2 {
				break
			}
			i += 2 + n
		}
	}
	return "", 0, 0, false
}
