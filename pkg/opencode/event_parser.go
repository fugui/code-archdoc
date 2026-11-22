package opencode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const maxJSONLScannerBytes = 8 * 1024 * 1024 // 8 MB，彻底防止长文 JSONL 行触发 bufio.ErrTooLong

// ParsedEventStream 汇总 OpenCode --format json 事件流解析产物。
type ParsedEventStream struct {
	SessionID         string
	FinalText         string
	InputTokens       int
	CachedInputTokens int
	OutputTokens      int
	StreamErrors      []string
}

type jsonlEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Error     *struct {
		Name    string `json:"name"`
		Message string `json:"message"`
		Data    *struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
	Message string `json:"message"`
	Part    *struct {
		Type      string `json:"type"`
		Text      string `json:"text"`
		MessageID string `json:"messageID"`
		Tokens    *struct {
			Input  int `json:"input"`
			Output int `json:"output"`
			Cache  *struct {
				Read  int `json:"read"`
				Write int `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
	} `json:"part"`
	Usage *struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		CachedInputTokens  int `json:"cached_input_tokens"`
		PromptTokens       int `json:"prompt_tokens"`
		CompletionTokens   int `json:"completion_tokens"`
		PromptCachedTokens int `json:"cached_tokens"`
	} `json:"usage"`
}

// ParseEventStream 使用 8MB 缓冲解析 OpenCode --format json 输出流，恢复 sessionID、末条助手消息并归一化 Usage。
func ParseEventStream(stdout []byte) ParsedEventStream {
	var out ParsedEventStream
	if len(stdout) == 0 {
		return out
	}

	var messageOrder []string
	messages := make(map[string]*strings.Builder)
	rawCacheRead := 0

	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 64*1024), maxJSONLScannerBytes)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev jsonlEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if sid := strings.TrimSpace(ev.SessionID); sid != "" {
			out.SessionID = sid
		}

		switch ev.Type {
		case "text":
			if ev.Part == nil || ev.Part.Type != "text" || ev.Part.Text == "" {
				continue
			}
			msgID := strings.TrimSpace(ev.Part.MessageID)
			if msgID == "" {
				msgID = "__default__"
			}
			sb, exists := messages[msgID]
			if !exists {
				sb = &strings.Builder{}
				messages[msgID] = sb
				messageOrder = append(messageOrder, msgID)
			}
			sb.WriteString(ev.Part.Text)

		case "step_finish":
			if ev.Part != nil && ev.Part.Tokens != nil {
				out.InputTokens += ev.Part.Tokens.Input
				out.OutputTokens += ev.Part.Tokens.Output
				if ev.Part.Tokens.Cache != nil && ev.Part.Tokens.Cache.Read > 0 {
					rawCacheRead += ev.Part.Tokens.Cache.Read
				}
			}
			if ev.Usage != nil {
				inTok := ev.Usage.InputTokens
				if inTok == 0 {
					inTok = ev.Usage.PromptTokens
				}
				outTok := ev.Usage.OutputTokens
				if outTok == 0 {
					outTok = ev.Usage.CompletionTokens
				}
				cached := ev.Usage.CachedInputTokens
				if cached == 0 {
					cached = ev.Usage.PromptCachedTokens
				}
				out.InputTokens += inTok
				out.OutputTokens += outTok
				rawCacheRead += cached
			}

		case "error":
			msg := strings.TrimSpace(ev.Message)
			if ev.Error != nil {
				if ev.Error.Data != nil && strings.TrimSpace(ev.Error.Data.Message) != "" {
					msg = strings.TrimSpace(ev.Error.Data.Message)
				} else if strings.TrimSpace(ev.Error.Message) != "" {
					msg = strings.TrimSpace(ev.Error.Message)
				} else if strings.TrimSpace(ev.Error.Name) != "" {
					msg = strings.TrimSpace(ev.Error.Name)
				}
			}
			if msg != "" {
				out.StreamErrors = append(out.StreamErrors, msg)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		out.StreamErrors = append(out.StreamErrors, fmt.Sprintf("JSONL 事件流扫描异常: %v", err))
	}

	if len(messageOrder) > 0 {
		out.FinalText = strings.TrimSpace(messages[messageOrder[len(messageOrder)-1]].String())
	}

	// Usage 归一化不变量：部分 Provider 将 cache.read 独立于 input 计量导致 cacheRead > input；
	// 此时自动归一化总输入为 input + cacheRead，严格保证 CachedInputTokens <= InputTokens。
	if rawCacheRead > 0 {
		if rawCacheRead > out.InputTokens {
			out.InputTokens += rawCacheRead
		}
		out.CachedInputTokens = rawCacheRead
		if out.CachedInputTokens > out.InputTokens {
			out.CachedInputTokens = out.InputTokens
		}
	}

	return out
}

func classifyProcessFailure(outcome processOutcome, parsed ParsedEventStream) error {
	if outcome.Err != nil {
		var ce *ClassifiedError
		if cls := ClassifyError(outcome.Err); cls != ErrorClassUnknown && cls != ErrorClassNone {
			if ce, ok := outcome.Err.(*ClassifiedError); ok {
				return ce
			}
			return &ClassifiedError{Class: cls, Message: outcome.Err.Error(), Cause: outcome.Err}
		}
		_ = ce
	}

	combined := strings.ToLower(strings.Join(append([]string{outcome.Stderr}, parsed.StreamErrors...), "\n"))
	if outcome.Err != nil {
		combined += "\n" + strings.ToLower(outcome.Err.Error())
	}

	switch {
	case strings.Contains(combined, "rate limit") ||
		strings.Contains(combined, "too many requests") ||
		strings.Contains(combined, "429"):
		return &ClassifiedError{Class: ErrorClassRateLimited, Message: summarizeFailureText(parsed, outcome), Cause: outcome.Err}

	case strings.Contains(combined, "unauthorized") ||
		strings.Contains(combined, "authentication") ||
		strings.Contains(combined, "invalid api key") ||
		strings.Contains(combined, "invalid_api_key") ||
		strings.Contains(combined, "401") ||
		strings.Contains(combined, "403") ||
		strings.Contains(combined, "model not found") ||
		strings.Contains(combined, "provider not found") ||
		strings.Contains(combined, "quota exceeded"):
		return &ClassifiedError{Class: ErrorClassAuth, Message: summarizeFailureText(parsed, outcome), Cause: outcome.Err}

	case strings.Contains(combined, "content filter") ||
		strings.Contains(combined, "safety") ||
		strings.Contains(combined, "blocked by policy"):
		return &ClassifiedError{Class: ErrorClassContentFiltered, Message: summarizeFailureText(parsed, outcome), Cause: outcome.Err}

	case strings.Contains(combined, "connection reset") ||
		strings.Contains(combined, "connection refused") ||
		strings.Contains(combined, "temporary failure") ||
		strings.Contains(combined, "econnreset") ||
		strings.Contains(combined, "unexpected eof"):
		return &ClassifiedError{Class: ErrorClassNetworkTransient, Message: summarizeFailureText(parsed, outcome), Cause: outcome.Err}

	default:
		return &ClassifiedError{Class: ErrorClassUnknown, Message: summarizeFailureText(parsed, outcome), Cause: outcome.Err}
	}
}

func summarizeFailureText(parsed ParsedEventStream, outcome processOutcome) string {
	if len(parsed.StreamErrors) > 0 {
		return strings.Join(parsed.StreamErrors, "; ")
	}
	trimmedStderr := strings.TrimSpace(outcome.Stderr)
	if trimmedStderr != "" {
		if len(trimmedStderr) > 512 {
			return trimmedStderr[len(trimmedStderr)-512:]
		}
		return trimmedStderr
	}
	if outcome.Err != nil {
		return outcome.Err.Error()
	}
	return fmt.Sprintf("OpenCode 进程异常退出 (exit=%d)", outcome.ExitCode)
}
