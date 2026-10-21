package db

// A verified parent parse may remove replay preceding the unchanged child
// suffix. The complete projection and its unique first-row anchor prove which
// prior occurrences survive. Digests alone never create message identity.
// Repeated anchors remain ambiguous; this deliberately prefers a visible gap.
func verifiedConversationSuffix(old, incoming []conversationRow) int {
	if len(incoming) == 0 || len(old) <= len(incoming) {
		return -1
	}
	offset := len(old) - len(incoming)
	for i := range incoming {
		if !conversationRowsEqual(old[offset+i], incoming[i]) {
			return -1
		}
	}
	matches := 0
	for _, row := range old {
		if conversationRowsEqual(row, incoming[0]) {
			matches++
		}
	}
	if matches != 1 {
		return -1
	}
	return offset
}
