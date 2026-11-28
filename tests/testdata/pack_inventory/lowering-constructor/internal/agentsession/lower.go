package agentsession

type Lowering struct{ word string }

func Any(word string) Lowering { return Lowering{word: word} }
