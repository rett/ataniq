package protocol

import core "ataniq/internal/agentsession/protocol"

// Decline is returned by Decode when a server request requires an immediate
// negative answer. The holder sends Reply as one line and continues reading.
// Reply contains the server's ID in its original wire type.
type Decline struct {
	Method string
	Reply  []byte
}

func (d *Decline) Error() string { return "app-server request declined: " + d.Method }

func decline(e envelope) error {
	var body string
	switch e.method {
	case permissionMethod:
		body = `"result":{"permissions":{},"scope":"turn"}`
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		body = `"result":{"decision":"decline"}`
	case "mcpServer/elicitation/request":
		body = `"result":{"action":"decline"}`
	case "item/tool/requestUserInput":
		// The response schema has no decline variant. Return a request error
		// so the server can finish without treating any answer as user input.
		body = `"error":{"code":-32000,"message":"User input declined"}`
	default:
		body = `"error":{"code":-32601,"message":"Unsupported server request"}`
	}
	// parse validated the raw string or integer ID. Echo it without a
	// conversion through float64 or any interpolation of decoded text.
	reply := []byte(`{"id":` + string(e.ID) + `,` + body + `}`)
	if len(reply) >= core.MaxFrameBytes {
		return core.ErrFrameTooLarge
	}
	return &Decline{Method: e.method, Reply: reply}
}
