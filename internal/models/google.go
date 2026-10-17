package models

type GoogleGenerateRequest struct {
	Contents          []GoogleContent   `json:"contents"`
	Tools             []GoogleTool      `json:"tools"`
	ToolConfig        *GoogleToolConfig `json:"toolConfig"`
	SystemInstruction *GoogleContent    `json:"systemInstruction"`
}

type GoogleContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GooglePart `json:"parts"`
}

type GooglePart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *GoogleInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *GoogleFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GoogleFunctionCallResp `json:"functionResponse,omitempty"`
}

type GoogleInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type GoogleFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type GoogleFunctionCallResp struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response,omitempty"`
}

type GoogleTool struct {
	FunctionDeclarations []GoogleFunctionDeclaration `json:"functionDeclarations"`
}

type GoogleFunctionDeclaration struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type GoogleToolConfig struct {
	FunctionCallingConfig *GoogleFunctionCallingConfig `json:"functionCallingConfig"`
}

type GoogleFunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type GoogleGenerateResponse struct {
	Candidates    []GoogleCandidate    `json:"candidates"`
	UsageMetadata *GoogleUsageMetadata `json:"usageMetadata,omitempty"`
	ModelVersion  string               `json:"modelVersion,omitempty"`
}

type GoogleCandidate struct {
	Index        int            `json:"index"`
	Content      *GoogleContent `json:"content,omitempty"`
	FinishReason string         `json:"finishReason,omitempty"`
}

type GoogleUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}
