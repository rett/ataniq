package desk

type extension struct{}

func (extension) Handle(op string) int {
	switch op {
	case "lead.read":
		return 1
	}
	return 0
}
func (extension) Operations() []string { return one() }
func one() []string                    { return two() }
func two() []string                    { return three() }
func three() []string                  { return four() }
func four() []string                   { return []string{"lead.read"} }
