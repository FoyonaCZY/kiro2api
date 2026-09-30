package kiro

import (
	"hash/crc32"
	"testing"
)

func TestCRC(t *testing.T) {
	if crc32.ChecksumIEEE([]byte("123456789")) != 0xCBF43926 {
		t.Fatal("crc")
	}
}

func TestDecodeSplitFramesAndRecover(t *testing.T) {
	a := EncodeEvent("assistantResponseEvent", map[string]any{"content": "Hel", "messageId": "x"})
	b := EncodeEvent("toolUseEvent", map[string]any{"name": "Read", "toolUseId": "t1", "input": "{\"a\"", "stop": true})
	c := EncodeEvent("contextUsageEvent", map[string]any{"contextUsagePercentage": 12.5})
	stream := append(append(append([]byte{}, a...), b...), c...)
	var d Decoder
	var events []*Event
	for i := 0; i < len(stream); i += 7 {
		end := i + 7
		if end > len(stream) {
			end = len(stream)
		}
		if err := d.Feed(stream[i:end]); err != nil {
			t.Fatal(err)
		}
		for {
			f, err := d.Next()
			if err != nil {
				t.Fatal(err)
			}
			if f == nil {
				break
			}
			ev, err := EventFromFrame(f)
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, ev)
		}
	}
	if len(events) != 3 || events[0].Content != "Hel" || events[1].ToolUseID != "t1" || !events[1].ToolStop || events[2].ContextUsage != 12.5 {
		t.Fatalf("%+v", events)
	}

	// 损坏 message CRC：整帧跳过，下一帧照常出来。
	bad := append([]byte{}, a...)
	bad[len(bad)-1] ^= 0xff
	var d2 Decoder
	d2.Feed(append(bad, c...))
	if _, err := d2.Next(); err == nil {
		t.Fatal("want crc error")
	}
	d2.Feed(nil)
	f, err := d2.Next()
	if err != nil || f == nil {
		t.Fatalf("recover failed: %v", err)
	}
	ev, _ := EventFromFrame(f)
	if ev.Kind != EvContextUsage {
		t.Fatal("wrong frame after recover")
	}

	exc := EncodeFrame([][2]string{{":message-type", "exception"}, {":exception-type", "ThrottlingException"}}, []byte("slow down"))
	var d3 Decoder
	d3.Feed(exc)
	f, _ = d3.Next()
	ev, _ = EventFromFrame(f)
	if ev.Kind != EvException || ev.Code != "ThrottlingException" || ev.Message != "slow down" {
		t.Fatalf("%+v", ev)
	}
}
