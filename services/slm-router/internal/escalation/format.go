package escalation

import "strconv"

// formatFloatPrec renders a float with three significant figures.
// Lives in its own file because the policy.go file would otherwise
// need to import strconv for this single helper.
func formatFloatPrec(f float64) string {
	return strconv.FormatFloat(f, 'g', 3, 64)
}
