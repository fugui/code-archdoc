package llm

import "strings"

const (
	// MarkerReply 为双段流式协议中自然语言解释段的起始标记（不透传给前端）。
	MarkerReply = "<<<ARCHDOC_REPLY>>>"
	// MarkerPayloadJSON 为双段流式协议中结构化 JSON 载荷段的起始标记（进入静默缓冲）。
	MarkerPayloadJSON = "<<<ARCHDOC_PAYLOAD_JSON>>>"
)

type splitFilterState int

const (
	splitStateTurnStart splitFilterState = iota
	splitStateReply
	splitStatePayload
)

// StreamSplitFilter 过滤 LLM 双段流式输出：
//  1. 自动剥离句首 <<<ARCHDOC_REPLY>>> 标记后实时透传自然语言回复增量；
//  2. 使用滑动前缀窗口识别跨分片的 <<<ARCHDOC_PAYLOAD_JSON>>> 标记，命中后进入静默缓冲并回调 onEnterPayload；
//  3. 兼容旧版直接以 '{' 开头的纯 JSON 响应，同样进入静默缓冲且不泄漏 JSON 文本；
//  4. 通过 ResetTurn() 在多轮只读工具调用间清空前一轮局部状态，防止工具轮碎片与最终轮粘连。
type StreamSplitFilter struct {
	state           splitFilterState
	buf             string
	emittedDelta    bool
	payloadNotified bool
	onDelta         func(string) error
	onEnterPayload  func()
}

// NewStreamSplitFilter 构造双段流式协议分片过滤器。
func NewStreamSplitFilter(onDelta func(string) error, onEnterPayload func()) *StreamSplitFilter {
	return &StreamSplitFilter{
		state:          splitStateTurnStart,
		onDelta:        onDelta,
		onEnterPayload: onEnterPayload,
	}
}

// Push 输入单个 LLM 流式文本增量，并按当前状态输出已确认安全的自然语言回复片段。
func (f *StreamSplitFilter) Push(delta string) error {
	if delta == "" || f.state == splitStatePayload {
		return nil
	}
	f.buf += delta
	return f.advance(false)
}

// ResetTurn 在多轮只读源码工具调用开启新一轮模型请求前重置单轮缓冲状态。
func (f *StreamSplitFilter) ResetTurn() {
	if f.state == splitStatePayload {
		return
	}
	f.buf = ""
	f.state = splitStateTurnStart
}

// Flush 在模型流式输出彻底结束时冲刷剩余非标记正文。
func (f *StreamSplitFilter) Flush() error {
	if f.state == splitStatePayload || f.buf == "" {
		f.buf = ""
		return nil
	}
	return f.advance(true)
}

// HasEmittedDelta 返回本次请求是否已向前端透传过至少一段自然语言回复文本。
func (f *StreamSplitFilter) HasEmittedDelta() bool {
	return f.emittedDelta
}

func (f *StreamSplitFilter) notifyEnterPayload() {
	if f.payloadNotified {
		return
	}
	f.payloadNotified = true
	if f.onEnterPayload != nil {
		f.onEnterPayload()
	}
}

func (f *StreamSplitFilter) emit(s string) error {
	if s == "" {
		return nil
	}
	f.emittedDelta = true
	if f.onDelta != nil {
		return f.onDelta(s)
	}
	return nil
}

func (f *StreamSplitFilter) advance(flush bool) error {
	if f.state == splitStateTurnStart {
		trimmed := strings.TrimLeft(f.buf, " \t\r\n")
		if trimmed == "" {
			if flush || len(f.buf) > 64 {
				f.buf = ""
				f.state = splitStateReply
			}
			return nil
		}

		// 1. 优先识别标准起始标记
		if strings.HasPrefix(trimmed, MarkerReply) {
			rest := strings.TrimPrefix(trimmed, MarkerReply)
			rest = strings.TrimPrefix(rest, "\r\n")
			rest = strings.TrimPrefix(rest, "\n")
			f.buf = rest
			f.state = splitStateReply
		} else if !flush && strings.HasPrefix(MarkerReply, trimmed) {
			f.buf = trimmed
			return nil
		} else if strings.HasPrefix(trimmed, MarkerPayloadJSON) {
			// 模型跳过了回复段直接输出载荷标记
			f.buf = ""
			f.state = splitStatePayload
			f.notifyEnterPayload()
			return nil
		} else if !flush && strings.HasPrefix(MarkerPayloadJSON, trimmed) {
			f.buf = trimmed
			return nil
		} else if strings.HasPrefix(trimmed, "{") {
			// 2. 兼容旧版或拒绝遵循双段指令、直接吐 JSON 的模型
			f.buf = ""
			f.state = splitStatePayload
			f.notifyEnterPayload()
			return nil
		} else {
			// 3. 普通自然语言开头（部分模型省略 <<<ARCHDOC_REPLY>>>，只输出结尾载荷标记）
			f.buf = trimmed
			f.state = splitStateReply
		}
	}

	if f.state == splitStateReply {
		if idx := strings.Index(f.buf, MarkerPayloadJSON); idx >= 0 {
			before := strings.TrimRight(f.buf[:idx], "\r\n")
			f.buf = ""
			f.state = splitStatePayload
			if err := f.emit(before); err != nil {
				return err
			}
			f.notifyEnterPayload()
			return nil
		}

		if flush {
			rest := f.buf
			f.buf = ""
			return f.emit(rest)
		}

		hold := longestSuffixPrefixLen(f.buf, MarkerPayloadJSON)
		if len(f.buf) > hold {
			emitPart := f.buf[:len(f.buf)-hold]
			f.buf = f.buf[len(f.buf)-hold:]
			return f.emit(emitPart)
		}
	}
	return nil
}

// longestSuffixPrefixLen 计算 s 的后缀与 target 的真前缀重叠的最大字节长度。
func longestSuffixPrefixLen(s, target string) int {
	maxOverlap := len(target) - 1
	if len(s) < maxOverlap {
		maxOverlap = len(s)
	}
	for k := maxOverlap; k > 0; k-- {
		if strings.HasSuffix(s, target[:k]) {
			return k
		}
	}
	return 0
}
