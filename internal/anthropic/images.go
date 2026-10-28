package anthropic

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/FoyonaCZY/kiro2api/internal/kiro"
)

const (
	// 只有最近几轮的图片保留，更早的换成占位文字。
	historyImageKeepTurns = 5
	// Anthropic 在一次请求超过 20 张图时要求每张边长不超过 2000px，
	// 否则整次请求 400（"many-image requests: 2000"）。总数压到 20 以内就不受这条限制。
	maxImagesPerRequest = 20
	omittedImagesFormat = "[This message contained %d image(s), omitted from older conversation history.]"
	// 工具结果里的图片挪到用户消息后，function_call_output 里留的说明。
	toolResultImagesFormat = "[%d image(s) returned by this tool are attached to the following user message.]"
	// 发不了的图片（URL、Files API、不支持的格式）和文档块换成占位文字，模型至少知道这里原来有东西。
	unsupportedImageFormat         = "[Image omitted: this image could not be passed to the model (%s).]"
	unsupportedDocumentFormat      = "[Document omitted: document attachments could not be passed to the model.]"
	unsupportedNamedDocumentFormat = "[Document omitted: %q could not be passed to the model.]"
)

func unsupportedImageNote(src *ImageSource) string {
	desc := "no source"
	if src != nil {
		desc = "type=" + src.Type
		if src.MediaType != "" {
			desc += ", media_type=" + src.MediaType
		}
		if src.URL != "" {
			desc += ", url=" + src.URL
		}
	}
	return fmt.Sprintf(unsupportedImageFormat, desc)
}

func unsupportedDocumentNote(title string) string {
	if strings.TrimSpace(title) == "" {
		return unsupportedDocumentFormat
	}
	return fmt.Sprintf(unsupportedNamedDocumentFormat, title)
}

// limitHistoryImages 就地裁掉历史里的旧图片，当前消息的图片不动，只用来算总数。
// 先按轮次裁：带工具结果的用户消息算在它所属的那一轮里，不开新一轮，
// 这样 Agent 连续调工具时，用户这一轮贴的图和工具读出来的图不会被挤掉。
// 当前轮加之前 historyImageKeepTurns-1 轮的图片保留，更早的全部去掉；
// 总数仍超过 maxImagesPerRequest 时，再从最旧的消息开始整条去掉。
func limitHistoryImages(history []kiro.HistoryMessage, cur *kiro.UserInputMessage) {
	var users []*kiro.UserMessage
	for _, h := range history {
		if h.User != nil {
			users = append(users, h.User)
		}
	}
	omitted := make(map[*kiro.UserMessage]int)
	// turn 是离当前轮隔了几轮。当前消息自己开了新一轮时，历史最后一条已经属于上一轮。
	turn := 0
	if len(cur.UserInputMessageContext.ToolResults) == 0 {
		turn = 1
	}
	total := len(cur.Images)
	for i := len(users) - 1; i >= 0; i-- {
		u := users[i]
		if len(u.Images) > 0 {
			if turn >= historyImageKeepTurns {
				omitted[u] += len(u.Images)
				u.Images = nil
			} else {
				total += len(u.Images)
			}
		}
		if u.UserInputMessageContext == nil || len(u.UserInputMessageContext.ToolResults) == 0 {
			turn++
		}
	}
	for _, u := range users {
		if total <= maxImagesPerRequest {
			break
		}
		if len(u.Images) == 0 {
			continue
		}
		total -= len(u.Images)
		omitted[u] += len(u.Images)
		u.Images = nil
	}
	count := 0
	for u, n := range omitted {
		count += n
		note := fmt.Sprintf(omittedImagesFormat, n)
		if strings.TrimSpace(u.Content) == "" {
			u.Content = note
		} else {
			u.Content += "\n" + note
		}
	}
	if count > 0 {
		slog.Info("历史消息里的旧图片已替换为占位文字", "omitted", count, "kept", total)
	}
}
