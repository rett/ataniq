package desk

import "inventory.test/internal/clientapi"

type capsExt[T any] struct{}

func (capsExt[T]) Operations() []string { return []string{"caps.enable"} }
func (*capsExt[T]) Handle(op string) int {
	if op == "caps.enable" {
		return 1
	}
	return 0
}

var registered clientapi.Extension = &capsExt[int]{}
