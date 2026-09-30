package anthropic

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/FoyonaCZY/kiro2api/internal/kiro"
)

const (
	// 与 sub2api 相同：只有最近几条用户消息保留图片，更早的换成占位文字。
	historyImageKeepMessages = 5
	// Anthropic 在一次请求超过 20 张图时要求每张边长不超过 2000px，
	// 否则整次请求 400（"many-image requests: 2000"）。总数压到 20 以内就不受这条限制。
	maxImagesPerRequest = 20
	omittedImagesFormat = "[This message contained %d image(s), omitted from older conversation history.]"
)

// limitHistoryImages 就地裁掉历史里的旧图片，当前消息的图片不动，currentImages 只用来算总数。
// 先按距离裁：从末尾往前数，第 historyImageKeepMessages 条用户消息之前的图片全部去掉；
// 总数仍超过 maxImagesPerRequest 时，再从最旧的消息开始整条去掉。
func limitHistoryImages(history []kiro.HistoryMessage, currentImages int) {
	var users []*kiro.UserMessage
	for _, h := range history {
		if h.User != nil {
			users = append(users, h.User)
		}
	}
	omitted := make(map[*kiro.UserMessage]int)
	// 当前消息算最近的一条，历史里还能保留 keep-1 条。
	keepFrom := len(users) - (historyImageKeepMessages - 1)
	total := currentImages
	for i, u := range users {
		if len(u.Images) == 0 {
			continue
		}
		if i < keepFrom {
			omitted[u] += len(u.Images)
			u.Images = nil
			continue
		}
		total += len(u.Images)
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
