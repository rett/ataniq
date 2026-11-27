package clientapi

import "encoding/json"

type Extension interface {
	Operations() []string
	Handle(op string, raw json.RawMessage) bool
}
