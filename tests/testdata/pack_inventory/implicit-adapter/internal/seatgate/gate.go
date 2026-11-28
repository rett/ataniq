package seatgate

type Handler interface{ Handle(string) int }
type HandlerFunc func(string) int

func (f HandlerFunc) Handle(op string) int { return f(op) }
