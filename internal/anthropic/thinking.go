package anthropic

import (
	"strings"
	"unicode/utf8"
)

// thinking 标签被这些字符包裹时视为引用，不当作真正的标签。
const quoteChars = "`\"'\\#!@$%^&*()-_=+[]{};:<>,.?/"

func isQuoteChar(s string, pos int) bool {
	return pos >= 0 && pos < len(s) && strings.IndexByte(quoteChars, s[pos]) >= 0
}

// charBoundary 小于等于 target 的最近 UTF-8 边界。
func charBoundary(s string, target int) int {
	if target >= len(s) {
		return len(s)
	}
	for target > 0 && !utf8.RuneStart(s[target]) {
		target--
	}
	return max(target, 0)
}

const (
	openTag  = "<thinking>"
	closeTag = "</thinking>"
)

func findTag(buf, tag string, accept func(pos, after int) (bool, bool)) int {
	start := 0
	for {
		i := strings.Index(buf[start:], tag)
		if i < 0 {
			return -1
		}
		pos := start + i
		after := pos + len(tag)
		if (pos > 0 && isQuoteChar(buf, pos-1)) || isQuoteChar(buf, after) {
			start = pos + 1
			continue
		}
		ok, stop := accept(pos, after)
		if ok {
			return pos
		}
		if stop {
			return -1
		}
		start = pos + 1
	}
}

// findRealThinkingEnd 真正的结束标签后面有 \n\n；标签后内容不足 2 字节时等待更多数据。
func findRealThinkingEnd(buf string) int {
	return findTag(buf, closeTag, func(_, after int) (bool, bool) {
		rest := buf[after:]
		if len(rest) < 2 {
			return false, true
		}
		return strings.HasPrefix(rest, "\n\n"), false
	})
}

// findRealThinkingEndAtBufferEnd 结束标签后只剩空白时也算结束（紧跟 tool_use 或流结束）。
func findRealThinkingEndAtBufferEnd(buf string) int {
	return findTag(buf, closeTag, func(_, after int) (bool, bool) {
		return strings.TrimSpace(buf[after:]) == "", false
	})
}

func findRealThinkingStart(buf string) int {
	return findTag(buf, openTag, func(int, int) (bool, bool) { return true, false })
}

// ExtractThinking 非流式：从完整文本里拆出 thinking 块。
func ExtractThinking(text string) (thinking string, ok bool, remaining string) {
	start := findRealThinkingStart(text)
	if start < 0 {
		return "", false, text
	}
	before := text[:start]
	afterOpen := text[start+len(openTag):]
	var raw, after string
	if end := findRealThinkingEnd(afterOpen); end >= 0 {
		raw, after = afterOpen[:end], afterOpen[end+len(closeTag)+2:]
	} else if end := findRealThinkingEndAtBufferEnd(afterOpen); end >= 0 {
		raw, after = afterOpen[:end], strings.TrimLeft(afterOpen[end+len(closeTag):], " \t\r\n")
	} else {
		return "", false, text
	}
	raw = strings.TrimPrefix(raw, "\n")
	if strings.TrimSpace(before) != "" {
		remaining = before
	}
	remaining += after
	if raw == "" {
		return "", false, remaining
	}
	return raw, true, remaining
}

// estimateTokens 流式输出的粗估：中文约 1.5 字/token，其余 4 字符/token。
func estimateTokens(text string) int {
	cn, other := 0, 0
	for _, r := range text {
		if r >= 0x4E00 && r <= 0x9FFF {
			cn++
		} else {
			other++
		}
	}
	return max((cn*2+2)/3+(other+3)/4, 1)
}
