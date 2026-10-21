package process

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// CheckLanguages checks that Tesseract has every language of langs
// (codes joined with "+", such as deu+eng), with tesseract --list-langs
// run in dir.
func CheckLanguages(ctx context.Context, r Runner, dir, langs string) error {
	out, err := r.Run(ctx, Command{Name: "tesseract", Args: []string{"--list-langs"}, Dir: dir})
	if err != nil {
		return fmt.Errorf("list the OCR languages: %w", err)
	}
	have := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		// The first line is a heading: List of available languages in "…" (3):
		if line != "" && !strings.Contains(line, " ") {
			have[line] = true
		}
	}
	var missing []string
	for _, l := range strings.Split(langs, "+") {
		if !have[l] {
			missing = append(missing, l)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("tesseract has no language data for %s (installed: %s)",
			strings.Join(missing, ", "), strings.Join(slices.Sorted(maps.Keys(have)), ", "))
	}
	return nil
}
