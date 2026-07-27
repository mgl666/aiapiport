package provider

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// ---- non-streaming response conversion ----

// anthropicResponse maps the fields we need from an Anthropic /v1/messages response.
type anthropicResponse struct {
	ID           string `json:"id"`
	Type         string `json:"type"` // "message"
	Role         string `json:"role"`
	Model        string `json:"model"`
	StopReason   string `json:"stop_reason"`
	StopSequence string `json:"stop_sequence"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Content []anthContentBlock `json:"content"`
}

type anthContentBlock struct {
	Type string `json:"type"` // "text" | "tool_use" | ...
	Text string `json:"text"`
}

// AnthropicToOpenAIResponse converts an Anthropic non-streaming response to an
// OpenAI chat/completions response.
func AnthropicToOpenAIResponse(body []byte, requestedModel string) ([]byte, error) {
	var ar anthropicResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, fmt.Errorf("parse anthropic response: %w", err)
	}

	var textBuilder strings.Builder
	for _, blk := range ar.Content {
		if blk.Type == "text" {
			textBuilder.WriteString(blk.Text)
		}
	}

	oai := map[string]any{
		"id":      "chatcmpl-" + ar.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   requestedModel,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": textBuilder.String(),
				},
				"finish_reason": convertStopReason(ar.StopReason),
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     ar.Usage.InputTokens,
			"completion_tokens": ar.Usage.OutputTokens,
			"total_tokens":      ar.Usage.InputTokens + ar.Usage.OutputTokens,
		},
	}
	return json.Marshal(oai)
}

func convertStopReason(r string) string {
	switch r {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return "stop"
	}
}

// ---- streaming response conversion ----

// ConvertAnthropicSSEStream reads an Anthropic SSE stream from reader, converts
// each event to OpenAI chat.completion.chunk format, and writes it to writer.
// Returns when the stream is exhausted.
//
// Anthropic SSE event types handled:
//
//	message_start               -> emit first chunk (with role)
//	content_block_delta         -> emit incremental content chunk (text_delta only)
//	message_stop                -> emit final chunk with finish_reason + [DONE]
//	message_delta               -> carries stop_reason used for finish_reason
func ConvertAnthropicSSEStream(reader io.Reader, writer io.Writer, requestedModel, completionID string) error {
	flusher, _ := writer.(interface{ Flush() })
	scanner := bufio.NewScanner(reader)
	// individual lines can be long (full text blocks); expand the buffer
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var index int64
	created := time.Now().Unix()
	started := false
	finishReason := "stop"

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			continue
		}

		var ev struct {
			Type  string          `json:"type"`
			Delta json.RawMessage `json:"delta"`
			Msg   struct {
				ID         string `json:"id"`
				Model      string `json:"model"`
				StopReason string `json:"stop_reason"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "message_start":
			if !started {
				started = true
				chunk := openAIChunk(completionID, requestedModel, created, index, &chunkDelta{Role: "assistant", Content: ""}, nil)
				if err := writeSSE(writer, chunk); err != nil {
					return err
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
		case "content_block_delta":
			// delta: { type:"text_delta", text:"..." }
			var d struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(ev.Delta, &d); err != nil {
				continue
			}
			if d.Type == "text_delta" && d.Text != "" {
				chunk := openAIChunk(completionID, requestedModel, created, index, &chunkDelta{Role: "assistant", Content: d.Text}, nil)
				index++
				if err := writeSSE(writer, chunk); err != nil {
					return err
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
		case "message_delta":
			if ev.Msg.StopReason != "" {
				finishReason = convertStopReason(ev.Msg.StopReason)
			}
		case "message_stop":
			chunk := openAIChunk(completionID, requestedModel, created, index, &chunkDelta{Role: "assistant", Content: ""}, &finishReason)
			if err := writeSSE(writer, chunk); err != nil {
				return err
			}
			if _, err := fmt.Fprint(writer, "data: [DONE]\n\n"); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("sse scan: %w", err)
	}
	return nil
}

// chunkDelta is the delta field inside an OpenAI stream chunk.
type chunkDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// openAIChunk builds an OpenAI chat.completion.chunk object and returns it as a JSON string.
func openAIChunk(id, model string, created int64, index int64, delta *chunkDelta, finish *string) string {
	choice := map[string]any{
		"index":         index,
		"delta":         delta,
		"finish_reason": nil,
	}
	if finish != nil {
		choice["finish_reason"] = *finish
	}
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{choice},
	}
	b, _ := json.Marshal(chunk)
	return string(b)
}

func writeSSE(w io.Writer, jsonData string) error {
	_, err := fmt.Fprintf(w, "data: %s\n\n", jsonData)
	return err
}
