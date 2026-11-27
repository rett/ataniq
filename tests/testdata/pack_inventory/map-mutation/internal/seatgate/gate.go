package seatgate

type Handler interface{ Handle(op string) int }
