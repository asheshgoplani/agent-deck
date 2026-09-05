package ui

import "strconv"

// Empty metadata means no explicit slot, not a claim about the running login.
func storedAccountLabel(account string) string {
	if account == "" {
		return "inherited"
	}
	return strconv.Quote(account)
}

// Quote before styling/truncation so terminal controls cannot become commands.
// Keep the delimiters visible even when a long account needs an ellipsis.
func storedAccountBadge(account string, budget int) string {
	const prefix = " [account:"
	label := storedAccountLabel(account)
	available := budget - cellWidth(prefix) - 1
	if available < 1 {
		return ""
	}
	if cellWidth(label) > available {
		if account != "" {
			if available < 3 {
				return ""
			}
			label = "\"" + cellTruncate(label[1:len(label)-1], available-2, "…") + "\""
		} else {
			label = cellTruncate(label, available, "…")
		}
	}
	return prefix + label + "]"
}
