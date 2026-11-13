package model

// Price is micro-USD per million tokens. Prices come from agent config,
// never from code, so they cannot go stale silently.
type Price struct {
	InputPerMTok      int64
	OutputPerMTok     int64
	CacheReadPerMTok  int64
	CacheWritePerMTok int64
}

// Cost returns the cost of u in micro-USD, rounded up. Negative token
// counts count as zero.
func (p Price) Cost(u Usage) int64 {
	sum := nonNeg(u.InputTokens)*p.InputPerMTok +
		nonNeg(u.OutputTokens)*p.OutputPerMTok +
		nonNeg(u.CacheReadTokens)*p.CacheReadPerMTok +
		nonNeg(u.CacheWriteTokens)*p.CacheWritePerMTok
	return (sum + 999_999) / 1_000_000
}

func nonNeg(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}
