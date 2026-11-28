package wire

type Operation string

const OpWithdraw Operation = "withdraw"

type Request struct {
	Op        Operation
	AttemptID string
	Text      string
}
