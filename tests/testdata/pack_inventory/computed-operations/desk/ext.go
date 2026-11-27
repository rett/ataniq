package desk

type extension struct{}

func (extension) Handle(op string) int {
	switch op {
	case "lead.read":
		return 1
	}
	return 0
}

var names = []string{"lead.read"}

func (extension) Operations() []string { return names }
