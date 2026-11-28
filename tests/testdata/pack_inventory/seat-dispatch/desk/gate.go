package desk

type gate struct{}

var caps = map[string]string{"seat.read": "view"}

func (gate) Handle(op string) int {
	if _, ok := caps[op]; !ok {
		return 0
	}
	switch op {
	case "seat.read", "seat.extra":
		return 1
	}
	return 0
}
