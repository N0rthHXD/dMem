package retrieval

import (
	"fmt"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

// ContextTokenFormatVersion is re-exported for callers that bind the rendering
// and estimation identity into a cursor or checkpoint.
const ContextTokenFormatVersion = domain.ContextTokenFormatVersion

// RenderContextPackage renders selected structured context as deterministic
// plain text for downstream generation. It states a date or speaker only when
// the selected item actually carries one; it never reconstructs conversation
// dates or speakers that were omitted from source authority.
func RenderContextPackage(pkg domain.ContextPackage) string {
	blocks := make([]string, 0, len(pkg.Items))
	for _, item := range pkg.Items {
		blocks = append(blocks, RenderContextItem(item))
	}
	return strings.Join(blocks, "\n\n")
}

// RenderContextItem renders one selected context item. The output is stable:
// identical input yields byte-identical text, and optional fields are omitted
// rather than fabricated.
func RenderContextItem(item domain.ContextItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%d] kind=%s confidence=%.3f lifecycle=%s tokens=%d", item.Rank, item.Kind, Clamp01(item.Confidence), item.LifecycleState, item.EstimatedTokens)
	if item.ClusterID != "" {
		fmt.Fprintf(&b, " cluster=%s", item.ClusterID)
	}
	b.WriteString("\n")
	if item.DisplayContent != "" {
		b.WriteString(item.DisplayContent)
		b.WriteString("\n")
	}
	if line := renderTimeLine("event_time", item.EventTimeStart, item.EventTimeEnd, item.TimePrecision); line != "" {
		b.WriteString(line)
		b.WriteString("\n")
	}
	if line := renderTimeLine("valid_time", item.ValidFrom, item.ValidUntil, ""); line != "" {
		b.WriteString(line)
		b.WriteString("\n")
	}
	if item.SourceSpeaker != "" {
		fmt.Fprintf(&b, "speaker: %s\n", item.SourceSpeaker)
	}
	if len(item.Evidence) > 0 {
		parts := make([]string, 0, len(item.Evidence))
		for _, evidence := range item.Evidence {
			part := evidence.EventID
			if evidence.Role != "" {
				part += " (" + string(evidence.Role) + ")"
			}
			if evidence.EventTime != nil {
				part += " @" + evidence.EventTime.UTC().Format(time.RFC3339Nano)
			}
			parts = append(parts, part)
		}
		fmt.Fprintf(&b, "evidence: %s\n", strings.Join(parts, ", "))
	}
	if item.GraphExplanation != "" {
		fmt.Fprintf(&b, "graph: %s\n", item.GraphExplanation)
	}
	if len(item.CorroboratingMemoryIDs) > 0 {
		fmt.Fprintf(&b, "corroborating: %s\n", strings.Join(item.CorroboratingMemoryIDs, ", "))
	}
	if len(item.SupplementaryMemoryIDs) > 0 {
		fmt.Fprintf(&b, "supplementary: %s\n", strings.Join(item.SupplementaryMemoryIDs, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderTimeLine(label string, start, end *time.Time, precision domain.TimePrecision) string {
	if start == nil && end == nil {
		return ""
	}
	format := func(value *time.Time) string {
		if value == nil {
			return "?"
		}
		return value.UTC().Format(time.RFC3339Nano)
	}
	line := label + ": " + format(start)
	if end != nil && (start == nil || !end.Equal(*start)) {
		line += ".." + format(end)
	}
	if precision != "" {
		line += " (" + string(precision) + ")"
	}
	return line
}
