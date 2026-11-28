package clientapi

type Extension interface {
	Operations() []string
	Handle(op string) int
}
