package leaddaemon

type ownerExt struct{}

func (ownerExt) Operations() []string { return []string{"owner.hook-path-on"} }
func (ownerExt) Handle(op string) int {
	switch op {
	case "owner.hook-path-on":
		return 1
	}
	return 0
}
