package bench

import (
	"fmt"
	"strings"
)

// Series is one named data series: a value per group, drawn in one color.
type Series struct {
	Name   string
	Values []float64
}

// chartColors cycles per series; enough for the three benchmark targets.
var chartColors = []string{"#4c78a8", "#f58518", "#54a24b", "#e45756", "#72b7b2"}

// BarChartSVG renders a grouped bar chart as a self-contained SVG string. groups
// labels the x-axis; each Series contributes one bar per group (values beyond
// len(groups) are ignored, missing ones are zero). It is pure text so charts can
// be checked into the repo and rendered by GitHub. Bars are scaled to the
// largest value across all series.
func BarChartSVG(title, yUnit string, groups []string, series []Series) string {
	const (
		w, h       = 760, 420
		padL, padR = 60, 20
		padT, padB = 50, 90
	)
	plotW := w - padL - padR
	plotH := h - padT - padB

	max := 0.0
	for _, s := range series {
		for _, v := range s.Values {
			if v > max {
				max = v
			}
		}
	}
	if max <= 0 {
		max = 1
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" font-family="sans-serif" font-size="12">`, w, h)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="white"/>`, w, h)
	fmt.Fprintf(&b, `<text x="%d" y="24" font-size="16" font-weight="bold">%s</text>`, padL, esc(title))
	if yUnit != "" {
		fmt.Fprintf(&b, `<text x="8" y="%d" font-size="11" fill="#555">%s</text>`, padT-8, esc(yUnit))
	}

	// Axes.
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#ccc"/>`, padL, padT, padL, padT+plotH)
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#ccc"/>`, padL, padT+plotH, padL+plotW, padT+plotH)

	// Gridlines and y labels at 0, 25, 50, 75, 100%.
	for i := 0; i <= 4; i++ {
		frac := float64(i) / 4
		y := padT + plotH - int(frac*float64(plotH))
		val := max * frac
		fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#eee"/>`, padL, y, padL+plotW, y)
		fmt.Fprintf(&b, `<text x="%d" y="%d" text-anchor="end" fill="#777">%s</text>`, padL-6, y+4, trimNum(val))
	}

	nGroups := len(groups)
	if nGroups == 0 {
		b.WriteString(`</svg>`)
		return b.String()
	}
	groupW := plotW / nGroups
	nSeries := len(series)
	barGap := 4
	barW := (groupW - barGap*(nSeries+1)) / max1(nSeries)

	for gi, g := range groups {
		gx := padL + gi*groupW
		for si, s := range series {
			v := 0.0
			if gi < len(s.Values) {
				v = s.Values[gi]
			}
			bh := int(v / max * float64(plotH))
			bx := gx + barGap + si*(barW+barGap)
			by := padT + plotH - bh
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" fill="%s"/>`,
				bx, by, barW, bh, chartColors[si%len(chartColors)])
		}
		fmt.Fprintf(&b, `<text x="%d" y="%d" text-anchor="middle" fill="#333">%s</text>`,
			gx+groupW/2, padT+plotH+18, esc(g))
	}

	// Legend.
	lx := padL
	ly := h - 24
	for si, s := range series {
		x := lx + si*140
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="12" height="12" fill="%s"/>`, x, ly-10, chartColors[si%len(chartColors)])
		fmt.Fprintf(&b, `<text x="%d" y="%d" fill="#333">%s</text>`, x+18, ly, esc(s.Name))
	}

	b.WriteString(`</svg>`)
	return b.String()
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// trimNum formats an axis value without a trailing ".0".
func trimNum(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	return strings.TrimSuffix(s, ".0")
}

func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
