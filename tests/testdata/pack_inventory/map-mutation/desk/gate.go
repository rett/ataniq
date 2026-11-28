package desk

type gate struct{}

var caps = map[string]string{"seat.read": "view"}

func init() { caps["seat.extra"] = "view" }
func (gate) Handle(op string) int {
	if _, ok := caps[op]; !ok {
		return 0
	}
	switch op {
	case "seat.read":
		return 1
	}
	return 0
}
