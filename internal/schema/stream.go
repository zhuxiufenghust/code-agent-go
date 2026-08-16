package schema

// StreamChunkType 枚举了 LLM 流式响应的增量 chunk 类型。
type StreamChunkType string

const (
	StreamChunkDone          StreamChunkType = "done"
	StreamChunkError         StreamChunkType = "error"
	StreamChunkTextDelta     StreamChunkType = "text_delta"
	StreamChunkThinkingDelta StreamChunkType = "thinking_delta"
)

// StreamChunk 是 LLM 流式响应的单个增量单元。
//
//	Type == text_delta     → Delta 有效（正文增量）
//	Type == thinking_delta → Delta 有效（推理增量）
//	Type == done           → Message、Usage 有效
//	Type == error          → Error 有效
type StreamChunk struct {
	Type    StreamChunkType `json:"type"`
	Delta   string          `json:"delta,omitempty"`
	Message *Message        `json:"message,omitempty"`
	Usage   *Usage          `json:"usage,omitempty"`
	Err     error           `json:"err,omitempty"`
}
