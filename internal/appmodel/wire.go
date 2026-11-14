package appmodel

import (
	"bytes"
	"encoding/json"
)

// object is a single shallow wire object, decoded only after scanJSON. Exact
// keys (including case) and non-null values are required at every schema level.
func object(raw []byte, keys ...string) (map[string]json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, invalid()
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != len(keys) {
		return nil, invalid()
	}
	for _, key := range keys {
		v, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, invalid()
		}
	}
	return fields, nil
}
func stringField(fields map[string]json.RawMessage, key string) (string, error) {
	var value string
	if json.Unmarshal(fields[key], &value) != nil {
		return "", invalid()
	}
	return value, nil
}

// Decode accepts exactly one bounded JSON object. Integer version tokens other
// than 1 report UnsupportedVersion after full syntax/scalar/duplicate checks
// and the required outer shape; their inner schema is not interpreted as v1.
// Fractional/exponent version tokens are InvalidInput, including 1.0 and 1e0.
func Decode(wire []byte) (Envelope, error) {
	if err := scanJSON(wire); err != nil {
		return Envelope{}, err
	}
	f, err := object(wire, "schema_version", "command_id", "project", "target", "operation", "expected_revision", "payload")
	if err != nil {
		return Envelope{}, err
	}
	version := string(f["schema_version"])
	if version == "" {
		return Envelope{}, invalid()
	}
	for i, c := range version {
		if c == '-' && i == 0 {
			continue
		}
		if c < '0' || c > '9' {
			return Envelope{}, invalid()
		}
	}
	if version != "1" {
		return Envelope{}, DomainError{Code: UnsupportedVersion}
	}
	var e Envelope
	e.SchemaVersion = SchemaVersion
	command, err := stringField(f, "command_id")
	if err != nil {
		return Envelope{}, err
	}
	e.CommandID = CommandID(command)
	revision, err := stringField(f, "expected_revision")
	if err != nil {
		return Envelope{}, err
	}
	e.ExpectedRevision = Revision(revision)
	project, err := object(f["project"], "authority", "id")
	if err != nil {
		return Envelope{}, err
	}
	e.Project.Authority, err = stringField(project, "authority")
	if err != nil {
		return Envelope{}, err
	}
	e.Project.ID, err = stringField(project, "id")
	if err != nil {
		return Envelope{}, err
	}
	operation, err := stringField(f, "operation")
	if err != nil {
		return Envelope{}, err
	}
	if !validID(operation) {
		return Envelope{}, invalid()
	}
	var target map[string]json.RawMessage
	switch operation {
	case "input.enqueue":
		target, err = object(f["target"], "authority", "kind", "id")
	case "queue.cancel":
		target, err = object(f["target"], "authority", "kind", "client_id", "id")
	default:
		return Envelope{}, DomainError{Code: UnsupportedCapability}
	}
	if err != nil {
		return Envelope{}, err
	}
	authority, err := stringField(target, "authority")
	if err != nil {
		return Envelope{}, err
	}
	id, err := stringField(target, "id")
	if err != nil {
		return Envelope{}, err
	}
	kind, err := stringField(target, "kind")
	if err != nil {
		return Envelope{}, err
	}
	if operation == "input.enqueue" {
		if kind != "conversation" {
			return Envelope{}, invalid()
		}
		payload, err := object(f["payload"], "text")
		if err != nil {
			return Envelope{}, err
		}
		text, err := stringField(payload, "text")
		if err != nil {
			return Envelope{}, err
		}
		e.Operation = Enqueue{Target: ConversationRef{Authority: authority, ID: id}, Payload: EnqueuePayload{Text: text}}
	} else {
		if kind != "command" {
			return Envelope{}, invalid()
		}
		if _, err := object(f["payload"]); err != nil {
			return Envelope{}, err
		}
		client, err := stringField(target, "client_id")
		if err != nil {
			return Envelope{}, err
		}
		e.Operation = Cancel{Target: CommandKey{Authority: authority, EnrolledClientID: client, CommandID: CommandID(id)}}
	}
	if err := e.Validate(); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

// Encode validates typed input and returns owned wire bytes. JSON formatting is
// not canonical binding. Neither JSON key order nor escape spelling is identity.
func Encode(e Envelope) ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	var target any
	var payload any
	var operation string
	switch op := e.Operation.(type) {
	case Enqueue:
		operation = "input.enqueue"
		target = struct {
			Authority string `json:"authority"`
			Kind      string `json:"kind"`
			ID        string `json:"id"`
		}{op.Target.Authority, "conversation", op.Target.ID}
		payload = struct {
			Text string `json:"text"`
		}{op.Payload.Text}
	case Cancel:
		operation = "queue.cancel"
		target = struct {
			Authority string    `json:"authority"`
			Kind      string    `json:"kind"`
			ClientID  string    `json:"client_id"`
			ID        CommandID `json:"id"`
		}{op.Target.Authority, "command", op.Target.EnrolledClientID, op.Target.CommandID}
		payload = struct{}{}
	}
	project := struct {
		Authority string `json:"authority"`
		ID        string `json:"id"`
	}{e.Project.Authority, e.Project.ID}
	wire, err := json.Marshal(struct {
		SchemaVersion    int       `json:"schema_version"`
		CommandID        CommandID `json:"command_id"`
		Project          any       `json:"project"`
		Target           any       `json:"target"`
		Operation        string    `json:"operation"`
		ExpectedRevision Revision  `json:"expected_revision"`
		Payload          any       `json:"payload"`
	}{e.SchemaVersion, e.CommandID, project, target, operation, e.ExpectedRevision, payload})
	if err != nil || len(wire) > MaxWireBytes {
		return nil, invalid()
	}
	return wire, nil
}
