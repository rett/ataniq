package clientapi

import "testing"

func TestDecodeRequestRefusesARepeatedKey(t *testing.T) {
	for name, tc := range map[string]struct {
		line string
		ok   bool
	}{
		"plain":            {`{"protocol":"ataniq.client/1","request_id":"a","op":"hello"}`, true},
		"spaced":           {` { "protocol" : "ataniq.client/1" , "request_id" : "a" , "op" : "status" } `, true},
		"same key nested":  {`{"protocol":"ataniq.client/1","request_id":"a","op":"hello","x":{"op":"a","op":"b"}}`, true},
		"same key twice":   {`{"protocol":"ataniq.client/1","request_id":"a","op":"hello","op":"hello"}`, false},
		"last differs":     {`{"protocol":"ataniq.client/1","request_id":"a","op":"approve","op":"hello"}`, false},
		"first differs":    {`{"protocol":"ataniq.client/1","request_id":"a","op":"hello","op":"approve"}`, false},
		"escaped spelling": {`{"protocol":"ataniq.client/1","request_id":"a","op":"hello","op":"hello"}`, false},
		"unknown key":      {`{"protocol":"ataniq.client/1","request_id":"a","op":"hello","z":1,"z":2}`, false},
		"case differs":     {`{"protocol":"ataniq.client/1","request_id":"a","op":"hello","Op":"x"}`, true},
	} {
		t.Run(name, func(t *testing.T) {
			id, op, fail := decodeRequest([]byte(tc.line))
			if tc.ok {
				if fail != nil || id != "a" || (op != OpHello && op != OpStatus) {
					t.Fatalf("id=%q op=%q fail=%+v", id, op, fail)
				}
				return
			}
			if fail == nil || fail.Code != CodeInvalidRequest || op != "" {
				t.Fatalf("op=%q fail=%+v", op, fail)
			}
		})
	}
}
