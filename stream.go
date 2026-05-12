package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// handleStreamProxy does SSE streaming with account failover.
func handleStreamProxy(w http.ResponseWriter, body []byte, isAnthropic bool, mgr *AccountManager) {
	accounts := mgr.getOrderedAccounts()
	if len(accounts) == 0 {
		http.Error(w, `{"error":"no account configured"}`, http.StatusBadRequest)
		return
	}

	for _, acc := range accounts {
		ok := doStreamProxy(w, acc, body, isAnthropic)
		if ok {
			mgr.failover(acc)
			return
		}
		fmt.Printf("[zed2api] stream: account '%s' failed, trying next...\n", acc.Name)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	w.Write([]byte(`{"error":{"message":"All accounts failed","type":"upstream_error"}}`))
}

// doStreamProxy attempts one streaming request to Zed for a single account.
// Returns true if we got useful data and sent it to the client.
func doStreamProxy(w http.ResponseWriter, acc *Account, body []byte, isAnthropic bool) bool {
	payload, err := buildZedPayload(body, isAnthropic)
	if err != nil {
		fmt.Printf("[stream] buildZedPayload failed: %v\n", err)
		return false
	}

	jwt, err := getToken(acc)
	if err != nil {
		fmt.Printf("[stream] getToken failed: %v\n", err)
		return false
	}

	resp, err := doZedRequestStream(jwt, payload)
	if err != nil {
		fmt.Printf("[stream] upstream request failed: %v\n", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("[stream] upstream HTTP %d\n", resp.StatusCode)
		return false
	}

	model := extractModelFromBody(body)
	model = normalizeModelName(model)
	chatID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixMilli())

	// Flush helper
	flusher, canFlush := w.(http.Flusher)

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.WriteHeader(http.StatusOK)

	// Send opening event
	if isAnthropic {
		modelJSON, _ := json.Marshal(model)
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_zed\",\"type\":\"message\",\"role\":\"assistant\",\"model\":%s,\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n", modelJSON)
	} else {
		modelJSON, _ := json.Marshal(model)
		chatIDJSON, _ := json.Marshal(chatID)
		fmt.Fprintf(w, "data: {\"id\":%s,\"object\":\"chat.completion.chunk\",\"model\":%s,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n", chatIDJSON, modelJSON)
	}
	if canFlush {
		flusher.Flush()
	}

	blockIndex := 0
	hasToolUse := false
	gotData := false

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line[0] != '{' {
			fmt.Printf("[stream] non-JSON from upstream (%d bytes): %s\n", len(line), truncate(line, 200))
			continue
		}
		gotData = true
		if err := convertAndSendSSE(w, line, &blockIndex, &hasToolUse, isAnthropic, model, chatID); err != nil {
			fmt.Printf("[stream] convertAndSendSSE error: %v\n", err)
			break
		}
		if canFlush {
			flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Printf("[stream] scanner error: %v\n", err)
	}

	// Send closing events
	if isAnthropic {
		stopReason := "end_turn"
		if hasToolUse {
			stopReason = "tool_use"
		}
		srJSON, _ := json.Marshal(stopReason)
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%s},\"usage\":{\"output_tokens\":1}}\n\n", srJSON)
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	} else {
		modelJSON, _ := json.Marshal(model)
		chatIDJSON, _ := json.Marshal(chatID)
		fmt.Fprintf(w, "data: {\"id\":%s,\"object\":\"chat.completion.chunk\",\"model\":%s,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", chatIDJSON, modelJSON)
	}
	if canFlush {
		flusher.Flush()
	}

	fmt.Printf("[stream] done, %d blocks\n", blockIndex)
	return gotData
}

// convertAndSendSSE converts a single Zed JSON line to SSE events and writes to w.
func convertAndSendSSE(w http.ResponseWriter, line string, blockIndex *int, hasToolUse *bool, isAnthropic bool, model, chatID string) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		return nil
	}

	// unwrap event wrapper
	if evRaw, ok := obj["event"]; ok {
		var inner map[string]json.RawMessage
		if json.Unmarshal(evRaw, &inner) == nil {
			obj = inner
		}
	}

	typeRaw, hasType := obj["type"]
	if hasType {
		var eventType string
		json.Unmarshal(typeRaw, &eventType)

		switch eventType {
		case "message_start":
			// log the model Zed returned
			if msgRaw, ok := obj["message"]; ok {
				var msg struct {
					Model string `json:"model"`
				}
				if json.Unmarshal(msgRaw, &msg) == nil && msg.Model != "" {
					fmt.Printf("[stream] zed returned model: %s\n", msg.Model)
				}
			}
			return nil

		case "content_block_start":
			if !isAnthropic {
				return nil
			}
			cbRaw, ok := obj["content_block"]
			if !ok {
				return nil
			}
			var cb struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if json.Unmarshal(cbRaw, &cb) != nil {
				return nil
			}
			if cb.Type == "tool_use" {
				*hasToolUse = true
				idJSON, _ := json.Marshal(cb.ID)
				nameJSON, _ := json.Marshal(cb.Name)
				fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,\"content_block\":{\"type\":\"tool_use\",\"id\":%s,\"name\":%s,\"input\":{}}}\n\n",
					*blockIndex, idJSON, nameJSON)
			} else {
				extra := `,"text":""`
				if cb.Type == "thinking" {
					extra = `,"thinking":""`
				}
				cbTypeJSON, _ := json.Marshal(cb.Type)
				fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,\"content_block\":{\"type\":%s%s}}\n\n",
					*blockIndex, cbTypeJSON, extra)
			}
			return nil

		case "content_block_delta":
			deltaRaw, ok := obj["delta"]
			if !ok {
				return nil
			}
			var delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
			}
			if json.Unmarshal(deltaRaw, &delta) != nil {
				return nil
			}

			if !isAnthropic {
				// OpenAI: only emit text_delta as choices[].delta.content
				if delta.Type == "text_delta" && delta.Text != "" {
					modelJSON, _ := json.Marshal(model)
					chatIDJSON, _ := json.Marshal(chatID)
					textJSON, _ := json.Marshal(delta.Text)
					fmt.Fprintf(w, "data: {\"id\":%s,\"object\":\"chat.completion.chunk\",\"model\":%s,\"choices\":[{\"index\":0,\"delta\":{\"content\":%s},\"finish_reason\":null}]}\n\n",
						chatIDJSON, modelJSON, textJSON)
				}
				return nil
			}
			// Anthropic passthrough
			deltaJSON, _ := json.Marshal(map[string]json.RawMessage{"delta": deltaRaw})
			_ = deltaJSON
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":%d,\"delta\":%s}\n\n",
				*blockIndex, deltaRaw)
			return nil

		case "content_block_stop":
			if !isAnthropic {
				*blockIndex++
				return nil
			}
			fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", *blockIndex)
			*blockIndex++
			return nil

		case "ping":
			if isAnthropic {
				fmt.Fprintf(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
			}
			return nil

		case "response.output_text.delta":
			// OpenAI responses format
			if deltaRaw, ok := obj["delta"]; ok {
				var text string
				if json.Unmarshal(deltaRaw, &text) == nil && text != "" {
					return emitTextDelta(w, text, blockIndex, isAnthropic, model, chatID)
				}
			}
			return nil
		}
	}

	// xAI (Grok): choices[]
	if choicesRaw, ok := obj["choices"]; ok {
		var choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		}
		if json.Unmarshal(choicesRaw, &choices) == nil && len(choices) > 0 && choices[0].Delta.Content != "" {
			return emitTextDelta(w, choices[0].Delta.Content, blockIndex, isAnthropic, model, chatID)
		}
		return nil
	}

	// Google (Gemini): candidates[]
	if candidatesRaw, ok := obj["candidates"]; ok {
		var candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		}
		if json.Unmarshal(candidatesRaw, &candidates) == nil && len(candidates) > 0 {
			for _, part := range candidates[0].Content.Parts {
				if part.Text != "" {
					if err := emitTextDelta(w, part.Text, blockIndex, isAnthropic, model, chatID); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}

	return nil
}

func emitTextDelta(w http.ResponseWriter, text string, blockIndex *int, isAnthropic bool, model, chatID string) error {
	textJSON, _ := json.Marshal(text)
	if !isAnthropic {
		modelJSON, _ := json.Marshal(model)
		chatIDJSON, _ := json.Marshal(chatID)
		fmt.Fprintf(w, "data: {\"id\":%s,\"object\":\"chat.completion.chunk\",\"model\":%s,\"choices\":[{\"index\":0,\"delta\":{\"content\":%s},\"finish_reason\":null}]}\n\n",
			chatIDJSON, modelJSON, textJSON)
		return nil
	}
	// Anthropic: emit content_block_start if first delta
	if *blockIndex == 0 {
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		*blockIndex = 1
	}
	fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", textJSON)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
