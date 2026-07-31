package bench

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestBarChartSVG_WellFormedAndLabeled(t *testing.T) {
	svg := BarChartSVG("Throughput", "tx/s", []string{"direct", "pgpilot", "pgbouncer"}, []Series{
		{Name: "read-heavy", Values: []float64{12000, 9000, 8500}},
	})

	// Parses as XML.
	if err := xml.Unmarshal([]byte(svg), new(struct {
		XMLName xml.Name
	})); err != nil {
		t.Fatalf("SVG is not well-formed XML: %v", err)
	}
	for _, want := range []string{"<svg", "Throughput", "tx/s", "direct", "pgpilot", "pgbouncer", "read-heavy", "<rect"} {
		if !strings.Contains(svg, want) {
			t.Errorf("SVG missing %q", want)
		}
	}
}

func TestBarChartSVG_MultiSeriesAndEscaping(t *testing.T) {
	svg := BarChartSVG("A & B <chart>", "", []string{"g1", "g2"}, []Series{
		{Name: "s1", Values: []float64{1, 2}},
		{Name: "s2", Values: []float64{3, 4}},
	})
	// Title special characters are escaped.
	if !strings.Contains(svg, "A &amp; B &lt;chart&gt;") {
		t.Errorf("title not escaped in:\n%s", svg)
	}
	// One bar per (group, series) = 4 data rects, plus the background rect.
	if n := strings.Count(svg, "<rect"); n < 5 {
		t.Errorf("expected >=5 rects (4 bars + background), got %d", n)
	}
}

func TestBarChartSVG_EmptyGroupsIsSafe(t *testing.T) {
	svg := BarChartSVG("Empty", "u", nil, nil)
	if !strings.HasPrefix(svg, "<svg") || !strings.HasSuffix(svg, "</svg>") {
		t.Errorf("empty chart not a valid svg document: %q", svg)
	}
}
