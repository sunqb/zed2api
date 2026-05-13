package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// normalizeModelName maps client model names to Zed-compatible names.
func normalizeModelName(name string) string {
	prefixes := []struct{ prefix, result string }{
		{"claude-opus-4-6", "claude-opus-4-6"},
		{"claude-opus-4-5", "claude-opus-4-5"},
		{"claude-opus-4-1", "claude-opus-4-1"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"claude-sonnet-4-5", "claude-sonnet-4-5"},
		{"claude-sonnet-4", "claude-sonnet-4"},
		{"claude-3-7-sonnet", "claude-3-7-sonnet"},
		{"claude-haiku-4-5", "claude-haiku-4-5"},
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p.prefix) {
			return p.result
		}
	}
	return name
}

// getProvider returns the Zed provider string for a model.
func getProvider(model string) string {
	switch {
	case strings.HasPrefix(model, "claude"):
		return "anthropic"
	case strings.HasPrefix(model, "gpt-"):
		return "open_ai"
	case strings.HasPrefix(model, "gemini"):
		return "google"
	case strings.HasPrefix(model, "grok"):
		return "x_ai"
	default:
		return "anthropic"
	}
}

// extractModelFromBody parses the model field from a request body.
func extractModelFromBody(body []byte) string {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		return "claude-sonnet-4-5"
	}
	return req.Model
}

// fakeUUID generates a random UUID-like string.
func fakeUUID() string {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, 16)
	rng.Read(b)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// buildZedPayload constructs the Zed completions request payload.
func buildZedPayload(body []byte, isAnthropic bool) ([]byte, error) {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse request body: %w", err)
	}

	modelRaw := parsed["model"]
	modelStr := "claude-sonnet-4-5"
	if modelRaw != nil {
		var m string
		if json.Unmarshal(modelRaw, &m) == nil && m != "" {
			modelStr = m
		}
	}
	model := normalizeModelName(modelStr)
	provider := getProvider(model)

	var sb strings.Builder
	sb.WriteString(`{"thread_id":"`)
	sb.WriteString(fakeUUID())
	sb.WriteString(`","prompt_id":"`)
	sb.WriteString(fakeUUID())
	sb.WriteString(`","intent":"user_prompt","provider":"`)
	sb.WriteString(provider)
	sb.WriteString(`","model":"`)
	sb.WriteString(model)
	sb.WriteString(`","provider_request":{`)

	var inner string
	var err error
	switch provider {
	case "anthropic":
		inner, err = buildAnthropicRequest(parsed, model, isAnthropic)
	case "open_ai":
		inner, err = buildOpenAIRequest(parsed, model, isAnthropic)
	case "google":
		inner, err = buildGoogleRequest(parsed, model, isAnthropic)
	default: // x_ai
		inner, err = buildXAIRequest(parsed, model, isAnthropic)
	}
	if err != nil {
		return nil, err
	}

	sb.WriteString(inner)
	sb.WriteString("}}")
	return []byte(sb.String()), nil
}

// extractSystemText pulls system text from Anthropic-format system field (string or array).
func extractSystemText(parsed map[string]json.RawMessage) string {
	raw, ok := parsed["system"]
	if !ok {
		return ""
	}
	// try string
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	// try array of {type, text}
	var arr []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &arr) == nil {
		var parts []string
		for _, item := range arr {
			if item.Text != "" {
				parts = append(parts, item.Text)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

func buildAnthropicRequest(parsed map[string]json.RawMessage, model string, isAnthropic bool) (string, error) {
	var sb strings.Builder
	sb.WriteString(`"model":`)
	modelJSON, _ := json.Marshal(model)
	sb.Write(modelJSON)
	sb.WriteString(",")

	if mt, ok := parsed["max_tokens"]; ok {
		sb.WriteString(`"max_tokens":`)
		sb.Write(mt)
	} else {
		sb.WriteString(`"max_tokens":8192`)
	}

	if isAnthropic {
		if sys := extractSystemText(parsed); sys != "" {
			sb.WriteString(`,"system":`)
			sysJSON, _ := json.Marshal(sys)
			sb.Write(sysJSON)
		}
	}

	if temp, ok := parsed["temperature"]; ok {
		sb.WriteString(`,"temperature":`)
		sb.Write(temp)
	}
	if thinking, ok := parsed["thinking"]; ok {
		sb.WriteString(`,"thinking":`)
		sb.Write(thinking)
	}

	// Tools
	if isAnthropic {
		if tools, ok := parsed["tools"]; ok {
			sb.WriteString(`,"tools":`)
			sb.Write(tools)
		}
		if tc, ok := parsed["tool_choice"]; ok {
			sb.WriteString(`,"tool_choice":`)
			sb.Write(tc)
		}
	} else {
		// OpenAI -> Anthropic tools conversion
		if toolsRaw, ok := parsed["tools"]; ok {
			var tools []struct {
				Function struct {
					Name        string          `json:"name"`
					Description string          `json:"description"`
					Parameters  json.RawMessage `json:"parameters"`
				} `json:"function"`
			}
			if json.Unmarshal(toolsRaw, &tools) == nil && len(tools) > 0 {
				sb.WriteString(`,"tools":[`)
				for i, t := range tools {
					if i > 0 {
						sb.WriteString(",")
					}
					sb.WriteString(`{"name":`)
					n, _ := json.Marshal(t.Function.Name)
					sb.Write(n)
					if t.Function.Description != "" {
						sb.WriteString(`,"description":`)
						d, _ := json.Marshal(t.Function.Description)
						sb.Write(d)
					}
					if len(t.Function.Parameters) > 0 {
						sb.WriteString(`,"input_schema":`)
						sb.Write(t.Function.Parameters)
					}
					sb.WriteString("}")
				}
				sb.WriteString("]")
			}
		}
		if tcRaw, ok := parsed["tool_choice"]; ok {
			var tcStr string
			if json.Unmarshal(tcRaw, &tcStr) == nil {
				switch tcStr {
				case "auto":
					sb.WriteString(`,"tool_choice":{"type":"auto"}`)
				case "required":
					sb.WriteString(`,"tool_choice":{"type":"any"}`)
				case "none":
					// omit
				}
			} else {
				sb.WriteString(`,"tool_choice":`)
				sb.Write(tcRaw)
			}
		}
	}

	// Messages
	sb.WriteString(`,"messages":[`)
	if msgsRaw, ok := parsed["messages"]; ok {
		var msgs []json.RawMessage
		if json.Unmarshal(msgsRaw, &msgs) == nil {
			for i, msg := range msgs {
				if i > 0 {
					sb.WriteString(",")
				}
			if isAnthropic {
				// Zed requires content to be an array, not a string.
				// Normalize string content -> [{type:text,text:...}]
				normalized, err := normalizeAnthropicMessage(msg)
				if err != nil {
					sb.Write(msg)
				} else {
					sb.WriteString(normalized)
				}
			} else {
				converted, err := convertOpenAIMessage(msg)
				if err != nil {
					sb.Write(msg)
				} else {
					sb.WriteString(converted)
				}
			}
			}
		}
	}
	sb.WriteString("]")
	return sb.String(), nil
}

// normalizeAnthropicMessage ensures message content is an array (Zed requirement).
// Converts string content -> [{"type":"text","text":"..."}]
func normalizeAnthropicMessage(raw json.RawMessage) (string, error) {
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return "", err
	}
	// Already an array — pass through as-is
	if len(msg.Content) > 0 && msg.Content[0] == '[' {
		return string(raw), nil
	}
	// String content — wrap in array
	var text string
	if err := json.Unmarshal(msg.Content, &text); err != nil {
		return string(raw), nil
	}
	roleJSON, _ := json.Marshal(msg.Role)
	textJSON, _ := json.Marshal(text)
	return fmt.Sprintf(`{"role":%s,"content":[{"type":"text","text":%s}]}`, roleJSON, textJSON), nil
}

// convertOpenAIMessage converts OpenAI-format messages to Anthropic format.
func convertOpenAIMessage(raw json.RawMessage) (string, error) {
	var msg struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id"`
		ToolCalls  json.RawMessage `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return string(raw), nil
	}

	// role=tool -> Anthropic tool_result
	if msg.Role == "tool" {
		idJSON, _ := json.Marshal(msg.ToolCallID)
		var contentStr string
		if json.Unmarshal(msg.Content, &contentStr) == nil {
			contentJSON, _ := json.Marshal(contentStr)
			return fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":%s,"content":%s}]}`,
				idJSON, contentJSON), nil
		}
		return fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":%s,"content":%s}]}`,
			idJSON, msg.Content), nil
	}

	// assistant with tool_calls -> Anthropic tool_use blocks
	if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
		var toolCalls []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		}
		if json.Unmarshal(msg.ToolCalls, &toolCalls) == nil {
			var sb strings.Builder
			sb.WriteString(`{"role":"assistant","content":[`)
			wrote := false
			// text content first
			var contentStr string
			if json.Unmarshal(msg.Content, &contentStr) == nil && contentStr != "" {
				sb.WriteString(`{"type":"text","text":`)
				t, _ := json.Marshal(contentStr)
				sb.Write(t)
				sb.WriteString("}")
				wrote = true
			}
			for _, tc := range toolCalls {
				if wrote {
					sb.WriteString(",")
				}
				wrote = true
				sb.WriteString(`{"type":"tool_use","id":`)
				idJSON, _ := json.Marshal(tc.ID)
				sb.Write(idJSON)
				sb.WriteString(`,"name":`)
				nameJSON, _ := json.Marshal(tc.Function.Name)
				sb.Write(nameJSON)
				sb.WriteString(`,"input":`)
				// parse arguments JSON string into object
				var inputObj json.RawMessage
				if json.Unmarshal([]byte(tc.Function.Arguments), &inputObj) == nil {
					sb.Write(inputObj)
				} else {
					sb.WriteString("{}")
				}
				sb.WriteString("}")
			}
			sb.WriteString("]}")
			return sb.String(), nil
		}
	}

	// default: convert content to Anthropic array format
	var contentStr string
	if json.Unmarshal(msg.Content, &contentStr) == nil {
		textJSON, _ := json.Marshal(contentStr)
		roleJSON, _ := json.Marshal(msg.Role)
		return fmt.Sprintf(`{"role":%s,"content":[{"type":"text","text":%s}]}`, roleJSON, textJSON), nil
	}
	// content is already array or complex
	roleJSON, _ := json.Marshal(msg.Role)
	return fmt.Sprintf(`{"role":%s,"content":%s}`, roleJSON, msg.Content), nil
}

func buildOpenAIRequest(parsed map[string]json.RawMessage, model string, isAnthropic bool) (string, error) {
	var sb strings.Builder
	modelJSON, _ := json.Marshal(model)
	sb.WriteString(`"model":`)
	sb.Write(modelJSON)
	sb.WriteString(`,"stream":true,"input":[`)

	wrote := false
	if isAnthropic {
		if sys := extractSystemText(parsed); sys != "" {
			sysJSON, _ := json.Marshal(sys)
			sb.WriteString(`{"type":"message","role":"system","content":[{"type":"input_text","text":`)
			sb.Write(sysJSON)
			sb.WriteString("}]}")
			wrote = true
		}
	}

	if msgsRaw, ok := parsed["messages"]; ok {
		var msgs []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(msgsRaw, &msgs) == nil {
			for _, msg := range msgs {
				if wrote {
					sb.WriteString(",")
				}
				wrote = true
				contentType := "input_text"
				if msg.Role == "assistant" {
					contentType = "output_text"
				}
				sb.WriteString(`{"type":"message","role":`)
				roleJSON, _ := json.Marshal(msg.Role)
				sb.Write(roleJSON)
				sb.WriteString(`,"content":[`)
				var contentStr string
				if json.Unmarshal(msg.Content, &contentStr) == nil {
					sb.WriteString(`{"type":"`)
					sb.WriteString(contentType)
					sb.WriteString(`","text":`)
					textJSON, _ := json.Marshal(contentStr)
					sb.Write(textJSON)
					sb.WriteString("}")
				} else {
					var contentArr []struct {
						Text string `json:"text"`
					}
					if json.Unmarshal(msg.Content, &contentArr) == nil {
						for i, item := range contentArr {
							if i > 0 {
								sb.WriteString(",")
							}
							sb.WriteString(`{"type":"`)
							sb.WriteString(contentType)
							sb.WriteString(`","text":`)
							textJSON, _ := json.Marshal(item.Text)
							sb.Write(textJSON)
							sb.WriteString("}")
						}
					}
				}
				sb.WriteString("]}")
			}
		}
	}
	sb.WriteString("]")
	return sb.String(), nil
}

func buildGoogleRequest(parsed map[string]json.RawMessage, model string, isAnthropic bool) (string, error) {
	var sb strings.Builder
	fullModel := "models/" + model
	modelJSON, _ := json.Marshal(fullModel)
	sb.WriteString(`"model":`)
	sb.Write(modelJSON)
	sb.WriteString(",")

	if isAnthropic {
		if sys := extractSystemText(parsed); sys != "" {
			sysJSON, _ := json.Marshal(sys)
			sb.WriteString(`"systemInstruction":{"parts":[{"text":`)
			sb.Write(sysJSON)
			sb.WriteString("}]},")
		}
	}
	sb.WriteString(`"generationConfig":{"candidateCount":1,"stopSequences":[],"temperature":1.0},`)

	// Tools
	if isAnthropic {
		if toolsRaw, ok := parsed["tools"]; ok {
			var tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"input_schema"`
			}
			if json.Unmarshal(toolsRaw, &tools) == nil && len(tools) > 0 {
				sb.WriteString(`"tools":[{"functionDeclarations":[`)
				for i, t := range tools {
					if i > 0 {
						sb.WriteString(",")
					}
					sb.WriteString(`{"name":`)
					n, _ := json.Marshal(t.Name)
					sb.Write(n)
					if t.Description != "" {
						sb.WriteString(`,"description":`)
						d, _ := json.Marshal(t.Description)
						sb.Write(d)
					}
					if len(t.InputSchema) > 0 {
						sb.WriteString(`,"parameters":`)
						sb.Write(t.InputSchema)
					}
					sb.WriteString("}")
				}
				sb.WriteString("]}],")
			}
		}
	} else {
		if toolsRaw, ok := parsed["tools"]; ok {
			var tools []struct {
				Function struct {
					Name        string          `json:"name"`
					Description string          `json:"description"`
					Parameters  json.RawMessage `json:"parameters"`
				} `json:"function"`
			}
			if json.Unmarshal(toolsRaw, &tools) == nil && len(tools) > 0 {
				sb.WriteString(`"tools":[{"functionDeclarations":[`)
				for i, t := range tools {
					if i > 0 {
						sb.WriteString(",")
					}
					sb.WriteString(`{"name":`)
					n, _ := json.Marshal(t.Function.Name)
					sb.Write(n)
					if t.Function.Description != "" {
						sb.WriteString(`,"description":`)
						d, _ := json.Marshal(t.Function.Description)
						sb.Write(d)
					}
					if len(t.Function.Parameters) > 0 {
						sb.WriteString(`,"parameters":`)
						sb.Write(t.Function.Parameters)
					}
					sb.WriteString("}")
				}
				sb.WriteString("]}],")
			}
		}
	}

	sb.WriteString(`"contents":[`)
	if msgsRaw, ok := parsed["messages"]; ok {
		var msgs []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(msgsRaw, &msgs) == nil {
			for i, msg := range msgs {
				if i > 0 {
					sb.WriteString(",")
				}
				geminiRole := msg.Role
				if msg.Role == "assistant" {
					geminiRole = "model"
				}
				sb.WriteString(`{"parts":[`)
				var contentStr string
				if json.Unmarshal(msg.Content, &contentStr) == nil {
					sb.WriteString(`{"text":`)
					textJSON, _ := json.Marshal(contentStr)
					sb.Write(textJSON)
					sb.WriteString("}")
				} else {
					var contentArr []struct {
						Text string `json:"text"`
					}
					if json.Unmarshal(msg.Content, &contentArr) == nil {
						for j, item := range contentArr {
							if j > 0 {
								sb.WriteString(",")
							}
							sb.WriteString(`{"text":`)
							textJSON, _ := json.Marshal(item.Text)
							sb.Write(textJSON)
							sb.WriteString("}")
						}
					}
				}
				sb.WriteString(`],"role":`)
				roleJSON, _ := json.Marshal(geminiRole)
				sb.Write(roleJSON)
				sb.WriteString("}")
			}
		}
	}
	sb.WriteString("]")
	return sb.String(), nil
}

func buildXAIRequest(parsed map[string]json.RawMessage, model string, isAnthropic bool) (string, error) {
	var sb strings.Builder
	modelJSON, _ := json.Marshal(model)
	sb.WriteString(`"model":`)
	sb.Write(modelJSON)
	sb.WriteString(`,"stream":true,`)

	if temp, ok := parsed["temperature"]; ok {
		sb.WriteString(`"temperature":`)
		sb.Write(temp)
	} else {
		sb.WriteString(`"temperature":1.0`)
	}
	sb.WriteString(`,"messages":[`)

	wrote := false
	if isAnthropic {
		if sys := extractSystemText(parsed); sys != "" {
			sysJSON, _ := json.Marshal(sys)
			sb.WriteString(`{"role":"system","content":`)
			sb.Write(sysJSON)
			sb.WriteString("}")
			wrote = true
		}
	}
	if msgsRaw, ok := parsed["messages"]; ok {
		var msgs []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(msgsRaw, &msgs) == nil {
			for _, msg := range msgs {
				if wrote {
					sb.WriteString(",")
				}
				wrote = true
				roleJSON, _ := json.Marshal(msg.Role)
				sb.WriteString(`{"role":`)
				sb.Write(roleJSON)
				sb.WriteString(`,"content":`)
				var contentStr string
				if json.Unmarshal(msg.Content, &contentStr) == nil {
					textJSON, _ := json.Marshal(contentStr)
					sb.Write(textJSON)
				} else {
					// extract text from array
					var arr []struct {
						Text string `json:"text"`
					}
					if json.Unmarshal(msg.Content, &arr) == nil {
						var parts []string
						for _, item := range arr {
							parts = append(parts, item.Text)
						}
						combined, _ := json.Marshal(strings.Join(parts, ""))
						sb.Write(combined)
					} else {
						sb.WriteString(`""`)
					}
				}
				sb.WriteString("}")
			}
		}
	}
	sb.WriteString("]")
	return sb.String(), nil
}

// ── Response conversion (non-streaming) ──

type streamContent struct {
	thinking  string
	text      string
	toolCalls []toolCallItem
}

type toolCallItem struct {
	ID        string
	Name      string
	Arguments string
}

// extractContentFromZedResponse parses accumulated Zed streaming lines into content.
func extractContentFromZedResponse(responseLines string) streamContent {
	var sc streamContent
	var currentToolID, currentToolName string
	var toolInputBuf strings.Builder
	inTool := false

	for _, line := range strings.Split(responseLines, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}

		// unwrap event wrapper
		if evRaw, ok := obj["event"]; ok {
			var inner map[string]json.RawMessage
			if json.Unmarshal(evRaw, &inner) == nil {
				obj = inner
			}
		}

		typeRaw, ok := obj["type"]
		if !ok {
			// check choices (xAI/OpenAI)
			if choicesRaw, ok := obj["choices"]; ok {
				var choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				}
				if json.Unmarshal(choicesRaw, &choices) == nil && len(choices) > 0 {
					sc.text += choices[0].Delta.Content
				}
				continue
			}
			// check candidates (Google)
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
						sc.text += part.Text
					}
				}
				continue
			}
			continue
		}

		var eventType string
		json.Unmarshal(typeRaw, &eventType)

		switch eventType {
		case "response.output_text.delta":
			if deltaRaw, ok := obj["delta"]; ok {
				var delta string
				if json.Unmarshal(deltaRaw, &delta) == nil {
					sc.text += delta
				}
			}

		case "content_block_start":
			if cbRaw, ok := obj["content_block"]; ok {
				var cb struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				}
				if json.Unmarshal(cbRaw, &cb) == nil && cb.Type == "tool_use" {
					currentToolID = cb.ID
					currentToolName = cb.Name
					toolInputBuf.Reset()
					inTool = true
				}
			}

		case "content_block_delta":
			if deltaRaw, ok := obj["delta"]; ok {
				var delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					Thinking    string `json:"thinking"`
					PartialJSON string `json:"partial_json"`
				}
				if json.Unmarshal(deltaRaw, &delta) == nil {
					switch delta.Type {
					case "text_delta":
						sc.text += delta.Text
					case "thinking_delta":
						sc.thinking += delta.Thinking
					case "input_json_delta":
						toolInputBuf.WriteString(delta.PartialJSON)
					}
				}
			}

		case "content_block_stop":
			if inTool && currentToolID != "" && currentToolName != "" {
				sc.toolCalls = append(sc.toolCalls, toolCallItem{
					ID:        currentToolID,
					Name:      currentToolName,
					Arguments: toolInputBuf.String(),
				})
				currentToolID = ""
				currentToolName = ""
				toolInputBuf.Reset()
				inTool = false
			}
		}
	}
	return sc
}

func convertToOpenAI(responseLines string, model string) ([]byte, error) {
	sc := extractContentFromZedResponse(responseLines)
	var sb strings.Builder
	sb.WriteString(`{"id":"chatcmpl-zed","object":"chat.completion","model":`)
	modelJSON, _ := json.Marshal(model)
	sb.Write(modelJSON)
	sb.WriteString(`,"choices":[{"index":0,"message":{"role":"assistant"`)

	if sc.thinking != "" {
		sb.WriteString(`,"thinking":`)
		thinkJSON, _ := json.Marshal(sc.thinking)
		sb.Write(thinkJSON)
	}

	if len(sc.toolCalls) > 0 && sc.text == "" {
		sb.WriteString(`,"content":null`)
	} else {
		sb.WriteString(`,"content":`)
		textJSON, _ := json.Marshal(sc.text)
		sb.Write(textJSON)
	}

	if len(sc.toolCalls) > 0 {
		sb.WriteString(`,"tool_calls":[`)
		for i, tc := range sc.toolCalls {
			if i > 0 {
				sb.WriteString(",")
			}
			idJSON, _ := json.Marshal(tc.ID)
			nameJSON, _ := json.Marshal(tc.Name)
			argsJSON, _ := json.Marshal(tc.Arguments)
			sb.WriteString(`{"id":`)
			sb.Write(idJSON)
			sb.WriteString(`,"type":"function","function":{"name":`)
			sb.Write(nameJSON)
			sb.WriteString(`,"arguments":`)
			sb.Write(argsJSON)
			sb.WriteString("}}")
		}
		sb.WriteString("]")
	}

	finishReason := "stop"
	if len(sc.toolCalls) > 0 {
		finishReason = "tool_calls"
	}
	sb.WriteString(`},"finish_reason":`)
	frJSON, _ := json.Marshal(finishReason)
	sb.Write(frJSON)
	sb.WriteString("}]}")
	return []byte(sb.String()), nil
}

func convertToAnthropic(responseLines string, model string) ([]byte, error) {
	sc := extractContentFromZedResponse(responseLines)
	var sb strings.Builder
	sb.WriteString(`{"id":"msg_zed","type":"message","role":"assistant","model":`)
	modelJSON, _ := json.Marshal(model)
	sb.Write(modelJSON)
	sb.WriteString(`,"content":[`)

	wrote := false
	if sc.thinking != "" {
		sb.WriteString(`{"type":"thinking","thinking":`)
		thinkJSON, _ := json.Marshal(sc.thinking)
		sb.Write(thinkJSON)
		sb.WriteString("}")
		wrote = true
	}
	if sc.text != "" {
		if wrote {
			sb.WriteString(",")
		}
		sb.WriteString(`{"type":"text","text":`)
		textJSON, _ := json.Marshal(sc.text)
		sb.Write(textJSON)
		sb.WriteString("}")
		wrote = true
	}
	for _, tc := range sc.toolCalls {
		if wrote {
			sb.WriteString(",")
		}
		wrote = true
		idJSON, _ := json.Marshal(tc.ID)
		nameJSON, _ := json.Marshal(tc.Name)
		var inputObj json.RawMessage
		if json.Unmarshal([]byte(tc.Arguments), &inputObj) != nil {
			inputObj = json.RawMessage("{}")
		}
		sb.WriteString(`{"type":"tool_use","id":`)
		sb.Write(idJSON)
		sb.WriteString(`,"name":`)
		sb.Write(nameJSON)
		sb.WriteString(`,"input":`)
		sb.Write(inputObj)
		sb.WriteString("}")
	}
	if !wrote {
		sb.WriteString(`{"type":"text","text":""}`)
	}

	stopReason := "end_turn"
	if len(sc.toolCalls) > 0 {
		stopReason = "tool_use"
	}
	sb.WriteString(`],"stop_reason":`)
	srJSON, _ := json.Marshal(stopReason)
	sb.Write(srJSON)
	sb.WriteString("}")
	return []byte(sb.String()), nil
}
