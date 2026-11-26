// Package protocol defines the messages of the Arshes Remote Editor protocol v2.
//
// Every WebSocket text frame carries exactly one JSON-RPC 2.0 message.
// Batches are not used.
package protocol

import (
	"encoding/json"
	"fmt"
)

// ProtocolVersion is the only protocol version this server speaks.
const ProtocolVersion = 2

// JSONRPCVersion is the value of the "jsonrpc" member of every message.
const JSONRPCVersion = "2.0"

// Error codes.
const (
	CodeParseError         = -32700
	CodeInvalidRequest     = -32600
	CodeMethodNotFound     = -32601
	CodeInvalidParams      = -32602
	CodeUnauthorized       = -32000
	CodeUnsupportedVersion = -32001
	CodeBusy               = -32002
	CodeTimeout            = -32003
)

// Methods.
const (
	// Arshes app → server request
	MethodHello = "hello"

	// Arshes app → server notifications
	MethodSendShader     = "sendShader"
	MethodSyncShaderSpec = "syncShaderSpec"

	// Server → Arshes app requests
	MethodCompileShader  = "compileShader"
	MethodCaptureImage   = "captureImage"
	MethodGetUniforms    = "getUniforms"
	MethodSetUniforms    = "setUniforms"
	MethodSetAspectRatio = "setAspectRatio"
	MethodGetDiagnostics = "getDiagnostics"
	MethodSetTouch       = "setTouch"
)

// Message is a JSON-RPC 2.0 message: a request, a notification or a response.
//
// The raw members keep the distinction between an absent member (nil) and
// an explicit JSON null ("null"), which JSON-RPC relies on.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// IsRequest reports whether the message is a request (has a method and an id).
func (m *Message) IsRequest() bool { return m.Method != "" && m.ID != nil }

// IsNotification reports whether the message is a notification (has a method but no id).
func (m *Message) IsNotification() bool { return m.Method != "" && m.ID == nil }

// IsResponse reports whether the message is a response (has an id, no method,
// and a result or an error).
func (m *Message) IsResponse() bool {
	return m.Method == "" && m.ID != nil && (m.Result != nil || m.Error != nil)
}

// Error is a JSON-RPC 2.0 error object. It also implements the error interface
// so that errors returned by the Arshes app can be passed around as Go errors.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if name := CodeName(e.Code); name != "" {
		return fmt.Sprintf("%s (%d): %s", name, e.Code, e.Message)
	}
	return fmt.Sprintf("error %d: %s", e.Code, e.Message)
}

// CodeName returns a short human readable name of an error code, or "" if unknown.
func CodeName(code int) string {
	switch code {
	case CodeParseError:
		return "parse error"
	case CodeInvalidRequest:
		return "invalid request"
	case CodeMethodNotFound:
		return "method not found"
	case CodeInvalidParams:
		return "invalid params"
	case CodeUnauthorized:
		return "unauthorized"
	case CodeUnsupportedVersion:
		return "unsupported version"
	case CodeBusy:
		return "busy"
	case CodeTimeout:
		return "timeout"
	}
	return ""
}

// NullID is the id used in an error response when the request id cannot be determined.
var NullID = json.RawMessage("null")

// NewRequest builds a request message.
func NewRequest(id int64, method string, params any) (*Message, error) {
	msg := &Message{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage(fmt.Sprintf("%d", id)),
		Method:  method,
	}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal params: %w", err)
		}
		msg.Params = raw
	}
	return msg, nil
}

// NewResult builds a success response message.
func NewResult(id json.RawMessage, result any) (*Message, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}
	return &Message{JSONRPC: JSONRPCVersion, ID: id, Result: raw}, nil
}

// NewErrorResponse builds an error response message. A nil id is sent as null.
func NewErrorResponse(id json.RawMessage, code int, message string) *Message {
	if id == nil {
		id = NullID
	}
	return &Message{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Error:   &Error{Code: code, Message: message},
	}
}

// HelloParams is the params of the hello request (Arshes app → server).
type HelloParams struct {
	ProtocolVersion *int   `json:"protocolVersion"`
	Token           string `json:"token"`
}

// HelloResult is the result of the hello request.
type HelloResult struct {
	ProtocolVersion int `json:"protocolVersion"`
}

// SendShaderParams is the params of the sendShader notification (Arshes app → server).
type SendShaderParams struct {
	Code string `json:"code"`
}

// CompileShaderParams is the params of the compileShader request.
type CompileShaderParams struct {
	Code string `json:"code"`
}

// CompileShaderResult is the result of the compileShader request.
type CompileShaderResult struct {
	Success      bool    `json:"success"`
	DurationMs   float64 `json:"durationMs"`
	ErrorMessage string  `json:"errorMessage,omitempty"`
}

// CaptureImageResult is the result of the captureImage request.
type CaptureImageResult struct {
	Image  string `json:"image"` // base64-encoded JPEG
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// UniformValues maps a custom uniform name to its value
// (a number, a bool, or an array of numbers).
type UniformValues map[string]json.RawMessage

// GetUniformsResult is the result of the getUniforms request.
type GetUniformsResult struct {
	Values UniformValues `json:"values"`
}

// SetUniformsParams is the params of the setUniforms request.
type SetUniformsParams struct {
	Values UniformValues `json:"values"`
}

// AspectRatios lists the values accepted by setAspectRatio.
var AspectRatios = []string{"1:1", "4:3", "16:9", "2.35:1", "full"}

// SetAspectRatioParams is the params of the setAspectRatio request.
type SetAspectRatioParams struct {
	AspectRatio string `json:"aspectRatio"`
}

// GetDiagnosticsResult is the result of the getDiagnostics request.
type GetDiagnosticsResult struct {
	FPS        float64   `json:"fps"`
	GPUTimeMs  *float64  `json:"gpuTimeMs,omitempty"`
	Resolution []float64 `json:"resolution"`
}

// SetTouchParams is the params of the setTouch request.
type SetTouchParams struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}
