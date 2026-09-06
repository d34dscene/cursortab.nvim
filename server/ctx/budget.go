package ctx

// Budget bounds the bytes context materials may add to a prompt. Collectors
// consume from it in the order the provider lists its materials, so the list
// order is the priority order.
type Budget struct {
	remaining int
	used      int
}

func NewBudget(chars int) *Budget {
	return &Budget{remaining: max(chars, 0)}
}

func (b *Budget) Remaining() int {
	return b.remaining
}

func (b *Budget) Used() int {
	return b.used
}

// Take consumes up to n bytes and returns how many were granted.
func (b *Budget) Take(n int) int {
	granted := min(max(n, 0), b.remaining)
	b.remaining -= granted
	b.used += granted
	return granted
}
