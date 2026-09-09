package format

import (
	"fmt"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

func ResponsesInputToMessages(input any, instructions string) ([]map[string]any, error) {
	var messages []map[string]any

	if instructions != "" {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": instructions,
		})
	}

	if strInput, ok := input.(string); ok {
		messages = append(messages, map[string]any{
			"role":    "user",
			"content": strInput,
		})
	} else if listInput, ok := input.([]any); ok {
		for _, item := range listInput {
			if strItem, ok := item.(string); ok {
				messages = append(messages, map[string]any{
					"role":    "user",
					"content": strItem,
				})
			} else if mapItem, ok := item.(map[string]any); ok {
				itemType, _ := mapItem["type"].(string)
				itemRole, _ := mapItem["role"].(string)

				if itemType == "function_call_output" {
					callID, _ := mapItem["call_id"].(string)
					name, _ := mapItem["name"].(string)
					output, _ := mapItem["output"].(string)
					messages = append(messages, map[string]any{
						"role":         "tool",
						"tool_call_id": callID,
						"name":         name,
						"content":      output,
					})
				} else if itemRole == "assistant" || (itemType == "message" && itemRole == "assistant") {
					rawContent := mapItem["content"]
					var textAcc string
					var tcList []map[string]any

					if cpList, ok := rawContent.([]any); ok {
						for _, cp := range cpList {
							if cpMap, ok := cp.(map[string]any); ok {
								cpType, _ := cpMap["type"].(string)
								if cpType == "output_text" {
									t, _ := cpMap["text"].(string)
									textAcc += t
								} else if cpType == "function_call" {
									tcList = append(tcList, cpMap)
								}
							}
						}
					} else if strCp, ok := rawContent.(string); ok {
						textAcc = strCp
					}

					m := map[string]any{
						"role":    "assistant",
						"content": textAcc,
					}
					if textAcc == "" {
						m["content"] = nil
					}

					if len(tcList) > 0 {
						var toolCalls []map[string]any
						for i, tc := range tcList {
							callID, _ := tc["call_id"].(string)
							if callID == "" {
								callID = fmt.Sprintf("call_%d", i)
							}
							name, _ := tc["name"].(string)
							args, _ := tc["arguments"].(string)
							if args == "" {
								args = "{}"
							}
							toolCalls = append(toolCalls, map[string]any{
								"id":   callID,
								"type": "function",
								"function": map[string]any{
									"name":      name,
									"arguments": args,
								},
							})
						}
						m["tool_calls"] = toolCalls
					}
					messages = append(messages, m)
				} else {
					role := itemRole
					if role == "" {
						role = "user"
					}
					// Preserve raw content (list or string) so downstream
					// MessagesToPrompt can extract image parts. Flattening to
					// a string here would silently drop image data.
					rawContent := mapItem["content"]
					if rawContent == nil {
						rawContent = ""
					}
					if cList, ok := rawContent.([]any); ok {
						// Normalize text-only lists to a joined string for
						// cleaner prompts, but keep the list when it contains
						// image parts.
						hasImage := false
						for _, c := range cList {
							if cMap, ok := c.(map[string]any); ok {
								if _, ok := ImageFromPart(cMap); ok {
									hasImage = true
									break
								}
							}
						}
						if !hasImage {
							var parts []string
							for _, c := range cList {
								if cMap, ok := c.(map[string]any); ok {
									cType, _ := cMap["type"].(string)
									if cType == "text" || cType == "input_text" || cType == "output_text" {
										if txt, ok := cMap["text"].(string); ok {
											parts = append(parts, txt)
										}
									}
								} else if s, ok := c.(string); ok {
									parts = append(parts, s)
								}
							}
							rawContent = strings.Join(parts, " ")
						}
					}

					messages = append(messages, map[string]any{
						"role":    role,
						"content": rawContent,
					})
				}
			}
		}
	}

	return messages, nil
}

func BuildResponseOutput(text string, toolCalls []models.OpenAIToolCall, msgID string) []map[string]any {
	var output []map[string]any

	if len(toolCalls) > 0 {
		for _, tc := range toolCalls {
			output = append(output, map[string]any{
				"type":      "function_call",
				"id":        tc.ID,
				"call_id":   tc.ID,
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
				"status":    "completed",
			})
		}
	}

	if text != "" || len(toolCalls) == 0 {
		output = append(output, map[string]any{
			"type":   "message",
			"id":     msgID,
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]any{
				{
					"type":        "output_text",
					"text":        text,
					"annotations": []any{},
				},
			},
		})
	}

	return output
}
