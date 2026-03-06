package protocol

// ServerMessage represents a message sent from CLI server to iPhone client
type ServerMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload,omitempty"`
}

// UpdateShaderPayload is the payload for updateShader messages
type UpdateShaderPayload struct {
	Code string `json:"code"`
}

// SyncShaderPayload is the payload for syncShader messages
type SyncShaderPayload struct {
	Code string `json:"code"`
}

// CompileResultPayload is the payload for compileResult messages
type CompileResultPayload struct {
	Success bool    `json:"success"`
	Error   *string `json:"error,omitempty"`
	Image   *string `json:"image,omitempty"` // base64-encoded JPEG
}

// NewUpdateShaderMessage creates a new updateShader message
func NewUpdateShaderMessage(code string) ServerMessage {
	return ServerMessage{
		Type: "updateShader",
		Payload: UpdateShaderPayload{
			Code: code,
		},
	}
}
