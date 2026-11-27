package protocol_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	core "ataniq/internal/agentsession/protocol"
	"ataniq/packs/codex/protocol"
)

func TestServerRequestDeclines(t *testing.T) {
	requests := frames(t, "server-requests")
	for i, tc := range []struct{ method, id, body string }{
		{"item/commandExecution/requestApproval", "9007199254740993", `"result":{"decision":"decline"}`},
		{"item/fileChange/requestApproval", `"7"`, `"result":{"decision":"decline"}`},
		{"item/tool/requestUserInput", `"input"`, `"error":{"code":-32000,"message":"User input declined"}`},
		{"mcpServer/elicitation/request", "14", `"result":{"action":"decline"}`},
		{"other", `"unknown"`, `"error":{"code":-32601,"message":"Unsupported server request"}`},
		{permissionName, "7", `"result":{"permissions":{},"scope":"turn"}`},
	} {
		t.Run(tc.method, func(t *testing.T) {
			c, _ := active(t)
			d, err := c.Decode(requests[i])
			var decline *protocol.Decline
			if !errors.As(err, &decline) || !reflect.DeepEqual(d, core.Decoded{}) || decline.Method != tc.method {
				t.Fatal("missing decline", d, err)
			}
			equalJSON(t, decline.Reply, `{"id":`+tc.id+`,`+tc.body+`}`)
			// The largest integer in this table loses precision under JCS.
			if !bytes.Contains(decline.Reply, []byte(tc.id)) {
				t.Fatalf("reply lost request ID %s: %s", tc.id, decline.Reply)
			}
		})
	}
}

func TestDeclineDoesNotAnswerOutstandingGrant(t *testing.T) {
	c, req := active(t)
	observePermission(t, c, req)
	for _, collision := range [][]byte{
		[]byte(`{"id":7,"method":"item/fileChange/requestApproval","params":{}}`),
		bytes.ReplaceAll(req, []byte("turn-a"), []byte("old-turn")),
	} {
		if _, err := c.Decode(collision); !errors.Is(err, core.ErrInvalidFrame) {
			t.Errorf("duplicate ID: %v", err)
		}
	}
	if _, err := c.EncodeGrant(req, protocol.Grant{Scope: "turn", Permissions: &protocol.Permissions{}}); err != nil {
		t.Fatal(err)
	}
}
