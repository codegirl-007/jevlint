package fix

import (
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

func UnifiedDiff(changes []FileChange) (string, error) {
	var output strings.Builder
	for _, change := range changes {
		diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A:        difflib.SplitLines(string(change.Before)),
			B:        difflib.SplitLines(string(change.After)),
			FromFile: "a/" + change.Path,
			ToFile:   "b/" + change.Path,
			Context:  3,
		})
		if err != nil {
			return "", err
		}
		output.WriteString(diff)
	}
	return output.String(), nil
}
