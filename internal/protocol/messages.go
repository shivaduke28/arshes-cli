package protocol

const ProtocolVersion = 1

// ServerMessage represents a message sent from server to client
type ServerMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload,omitempty"`
}

// HelloPayload is the payload for hello messages (client → server)
type HelloPayload struct {
	ProtocolVersion int    `json:"protocolVersion"`
	Token           string `json:"token"`
}

// HelloResultPayload is the payload for helloResult messages (server → client)
type HelloResultPayload struct {
	Code            string `json:"code"`
	ProtocolVersion int    `json:"protocolVersion,omitempty"`
	Message         string `json:"message,omitempty"`
}

// CompileShaderPayload is the payload for compileShader messages (server → client)
type CompileShaderPayload struct {
	Code  string `json:"code"`
	Image bool   `json:"image,omitempty"`
}

// CompileResultPayload is the payload for compileResult messages (client → server)
type CompileResultPayload struct {
	Success      bool    `json:"success"`
	ErrorMessage *string `json:"errorMessage,omitempty"`
	Image        *string `json:"image,omitempty"` // base64-encoded JPEG
}

// SendShaderPayload is the payload for sendShader messages (client → server)
type SendShaderPayload struct {
	Code string `json:"code"`
}

// NewHelloResultMessage creates a helloResult message for successful handshake
func NewHelloResultMessage(code string, protocolVersion int, message string) ServerMessage {
	payload := HelloResultPayload{
		Code: code,
	}
	if code == "ok" {
		payload.ProtocolVersion = protocolVersion
	} else {
		payload.Message = message
	}
	return ServerMessage{
		Type:    "helloResult",
		Payload: payload,
	}
}

// NewCompileShaderMessage creates a new compileShader message
func NewCompileShaderMessage(code string, image bool) ServerMessage {
	return ServerMessage{
		Type: "compileShader",
		Payload: CompileShaderPayload{
			Code:  code,
			Image: image,
		},
	}
}
