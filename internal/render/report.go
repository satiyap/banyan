package render

// The monthly report as an A4 PDF, rendered by the same engine that renders
// cheques. This is paperwork for a committee, not a leaf: nothing here touches
// the cheque geometry, and a report never goes near the printer.

import (
	"encoding/base64"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/satiyap/banyan/internal/reports"
	"github.com/satiyap/banyan/internal/webpdf"
)

// Same slots as the desk (year.js HEAD_COLOURS), in head order.
var colours = []string{"#34548a", "#2f7d74", "#a07a1c", "#7a4e8c", "#a8503a", "#4f7a4a", "#5f7896"}

const otherColour = "#9aa3a8"

var monthNames = []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;")

// e is Python's html.escape, quotes included.
func e(text string) string { return htmlEscaper.Replace(text) }

// fixed is Python's {x:.Nf}. FormatFloat rounds the exact binary value, ties to even, as Python does.
func fixed(x float64, digits int) string { return strconv.FormatFloat(x, 'f', digits, 64) }

func inr(paise int64) string {
	abs := paise
	if abs < 0 {
		abs = -abs
	}
	s := strconv.FormatInt(abs/100, 10)
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		s = strings.Join(parts, ",") + "," + tail
	}
	sign := ""
	if paise < 0 {
		sign = "-"
	}
	return sign + "₹" + s
}

func money(paise int64) string {
	if paise == 0 {
		return `<span class="nil">–</span>`
	}
	return inr(paise)
}

func lakhs(paise int64) string {
	r := float64(paise) / 100
	if r >= 1e7 {
		return "₹" + fixed(r/1e7, 1) + " crore"
	}
	if r >= 1e5 {
		return "₹" + fixed(r/1e5, 1) + " lakh"
	}
	return inr(paise)
}

func plural(n int) string {
	if n != 1 {
		return "s"
	}
	return ""
}

var spaceRe = regexp.MustCompile(`\s+`)

// short is "Apr 2026" as "Apr 26".
func short(m reports.MonthlyMonth) string {
	parts := spaceRe.Split(m.Label, -1)
	if len(parts) < 2 {
		return m.Label
	}
	yr := parts[1]
	if len(yr) > 2 {
		yr = yr[2:]
	}
	return parts[0] + " " + yr
}

// BuildReportHTML is the whole report as one self-contained page.
func (r *Renderer) BuildReportHTML(d *reports.MonthlyReport, today time.Time) (string, error) {
	colour := map[string]string{}
	for i, x := range d.Heads {
		if i < len(colours) {
			colour[x.Head] = colours[i]
		} else {
			colour[x.Head] = otherColour
		}
	}
	var begun []reports.MonthlyMonth
	for _, m := range d.Months {
		if !m.Future {
			begun = append(begun, m)
		}
	}
	var months []reports.MonthlyMonth
	for i, m := range begun {
		if m.Count != 0 {
			months = begun[i:]
			break
		}
	}
	heads := d.Heads
	var paying []reports.MonthlyHead
	for _, x := range d.Heads {
		if x.Paise != 0 {
			paying = append(paying, x)
		}
	}
	if len(paying) > 0 {
		heads = paying
	}
	t := d.Totals
	sw := func(h string) string { return `<i class="sw" style="background:` + colour[h] + `"></i>` }

	span := d.Label
	if len(months) > 0 {
		span = months[0].Label + " to " + months[len(months)-1].Label
	}
	title := "The last six months"
	if d.Mode == "fy" {
		title = d.Label
	}
	var th, body, foot, bars, legend, detail strings.Builder
	for _, m := range months {
		note := ""
		if m.Current {
			note = "<small>to date</small>"
		}
		th.WriteString(`<th class="num">` + short(m) + note + `</th>`)
		foot.WriteString(`<td class="num">` + money(m.Paise) + `</td>`)
	}
	for _, x := range heads {
		body.WriteString(`<tr><th>` + sw(x.Head) + e(x.Head) + `</th>`)
		for _, m := range months {
			body.WriteString(`<td class="num">` + money(m.Heads.Get(x.Head)) + `</td>`)
		}
		body.WriteString(`<td class="num strong">` + money(x.Paise) + `</td></tr>`)
		legend.WriteString(`<span>` + sw(x.Head) + e(x.Head) + `</span>`)
	}
	top := int64(1)
	if len(months) > 0 {
		top = math.MinInt64
		for _, m := range months {
			top = max(top, m.Paise)
		}
		if top == 0 {
			top = 1
		}
	}
	for _, m := range months {
		bars.WriteString(`<div class="bar-row"><span class="bl">` + short(m) + `</span><div class="bar">`)
		for _, x := range heads {
			if amt := m.Heads.Get(x.Head); amt != 0 {
				bars.WriteString(`<span style="background:` + colour[x.Head] + `;width:` +
					fixed(float64(amt)/float64(top)*100, 2) + `%"></span>`)
			}
		}
		bars.WriteString(`</div><span class="num">` + money(m.Paise) + `</span></div>`)
	}
	for i := len(months) - 1; i >= 0; i-- {
		m := months[i]
		if m.Count == 0 {
			continue
		}
		var groups strings.Builder
		for _, x := range heads {
			amt := m.Heads.Get(x.Head)
			if amt == 0 {
				continue
			}
			groups.WriteString(`<div class="grp"><h4>` + sw(x.Head) + e(x.Head) +
				`<span class="num">` + inr(amt) + `</span></h4><ul>`)
			for _, n := range m.Entities {
				if n.Head == x.Head {
					groups.WriteString(`<li><span>` + e(n.Name) + `</span><span class="num">` + inr(n.Paise) + `</span></li>`)
				}
			}
			groups.WriteString(`</ul></div>`)
		}
		note := ""
		if m.Current {
			note = "<small>month to date</small>"
		}
		detail.WriteString(`<section class="month"><header><h3>` + e(m.Label) + note + `</h3>` +
			`<p><b>` + inr(m.Paise) + `</b> across ` + strconv.Itoa(m.Count) + ` cheque` + plural(m.Count) + `</p>` +
			`</header><div class="grps">` + groups.String() + `</div></section>`)
	}

	lede := "No cheques in this period."
	if t.Count != 0 {
		lede = fmt.Sprintf("%s a month on average, over the %d month%s with cheques.",
			lakhs(t.PerMonthPaise), t.MonthsPaid, plural(t.MonthsPaid))
	}
	// The fonts travel inside the page: it is opened as a lone file.
	var fonts strings.Builder
	for _, f := range []struct {
		family string
		weight int
		file   string
	}{
		{"Hedvig Letters Serif", 400, "hedvig-letters-serif-latin-400-normal.woff2"},
		{"Geist", 400, "geist-sans-latin-400-normal.woff2"},
		{"Geist", 500, "geist-sans-latin-500-normal.woff2"},
		{"Geist", 600, "geist-sans-latin-600-normal.woff2"},
	} {
		data, err := fs.ReadFile(r.Assets, "static/fonts/"+f.file)
		if err != nil {
			return "", Error("the app is missing the font %s", f.file)
		}
		fmt.Fprintf(&fonts, `@font-face{font-family:"%s";font-weight:%d;src:url("data:font/woff2;base64,%s")}`,
			f.family, f.weight, base64.StdEncoding.EncodeToString(data))
	}
	generated := fmt.Sprintf("%02d %s %d", today.Day(), monthNames[today.Month()-1], today.Year())
	eyebrow := e(span)
	if d.Mode == "fy" {
		eyebrow += ", April to March"
	}

	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Monthly spend by head</title><style>
` + fonts.String() + reportCSS + `
</style></head><body>
<div class="mast"><b>Banyan</b><span>Monthly spend by head · generated ` + generated + `</span></div>
<p class="eyebrow">` + eyebrow + `</p>
<h1>` + e(title) + `: ` + lakhs(t.Paise) + ` paid</h1><p class="lede">` + lede + `</p>
<div class="kpis"><div><b>` + inr(t.Paise) + `</b><span>total paid</span></div>
<div><b>` + inr(t.PerMonthPaise) + `</b><span>a month on average</span></div>
<div><b>` + strconv.Itoa(t.Count) + `</b><span>cheques</span></div></div>
<div class="keep"><h2>Month by head</h2><table><thead><tr><th>Head</th>` + th.String() + `<th class="num">Total</th></tr></thead>
<tbody>` + body.String() + `</tbody><tfoot><tr><th>Total</th>` + foot.String() + `<td class="num">` + money(t.Paise) + `</td></tr></tfoot></table></div>
<div class="keep"><h2>How each month splits</h2><div class="legend">` + legend.String() + `</div>` + bars.String() + `</div>
<h2>Month by month</h2>` + detail.String() + `
<p class="foot">From the cheque register: issued cheques only (printed, logged and queued); void and failed cheques are left out. Amounts by cheque date, in whole rupees.</p>
</body></html>`, nil
}

// The sheet's margins are set on the page handed to the engine, which is what
// WebKit honours.
var reportPage = webpdf.Page{W: 210, H: 297, Paged: true, Margins: [4]float64{16, 15, 18, 15}}

const reportCSS = `
:root{--ink:#16202e;--ink-2:#3d4754;--muted:#6a737d;--rule:#d3d9dc;--zebra:#f3f6f7}
*{box-sizing:border-box;-webkit-print-color-adjust:exact;print-color-adjust:exact}
body{margin:0;color:var(--ink);font:9.5pt/1.45 "Geist",sans-serif;font-variant-numeric:tabular-nums}
h1,h2,h3{font-family:"Hedvig Letters Serif",serif;font-weight:400;letter-spacing:-.015em;margin:0}
.mast{display:flex;justify-content:space-between;align-items:baseline;border-bottom:2px solid var(--ink);padding-bottom:6pt;color:var(--ink-2);font-size:8.5pt}
.mast b{font:400 11pt "Hedvig Letters Serif",serif;color:var(--ink)}
.eyebrow{margin:16pt 0 2pt;color:var(--ink-2);font-weight:500}
h1{font-size:24pt;line-height:1.1}.lede{color:var(--ink-2);margin:4pt 0 0}
.kpis{display:flex;gap:28pt;margin:14pt 0 4pt}.kpis div{display:flex;flex-direction:column}
.kpis b{font:400 16pt "Hedvig Letters Serif",serif}.kpis span{color:var(--muted);font-size:8pt}
h2{font-size:13pt;margin:20pt 0 7pt}
.sw{display:inline-block;width:7pt;height:7pt;border-radius:1.5pt;margin-right:5pt}
table{width:100%;border-collapse:collapse}th,td{padding:5pt 6pt;border-bottom:.5pt solid var(--rule);text-align:left}
thead th{font-weight:500;color:var(--ink-2);font-size:8pt;vertical-align:bottom}
tbody th{font-weight:500}tbody tr:nth-child(even){background:var(--zebra)}
tfoot td,tfoot th{font-weight:600;border-top:1.2pt solid var(--ink);border-bottom:0}
.num{text-align:right;white-space:nowrap}.strong{font-weight:600}.nil{color:var(--muted)}
small{display:block;font-weight:400;font-size:7pt;color:var(--muted)}
.legend{display:flex;gap:12pt;color:var(--ink-2);font-size:8pt;margin-bottom:6pt}
.bar-row{display:grid;grid-template-columns:40pt 1fr 62pt;gap:8pt;align-items:center;margin:4pt 0}
.bl{color:var(--ink-2);font-size:8.5pt}.bar{display:flex;height:11pt}.bar span+span{border-left:1pt solid #fff}
.month{border-top:.5pt solid var(--rule);padding:10pt 0 4pt;break-inside:avoid}
.month header{display:flex;justify-content:space-between;align-items:baseline}
.month h3{font-size:12pt}.month h3 small{display:inline;margin-left:6pt;font-family:"Geist",sans-serif}
.month p{margin:0;color:var(--muted);font-size:8.5pt}.month p b{font:400 11pt "Hedvig Letters Serif",serif;color:var(--ink)}
.grps{columns:2;column-gap:22pt;margin-top:6pt}.grp{break-inside:avoid;margin-bottom:8pt}
h4{display:flex;align-items:center;margin:0 0 2pt;font-size:9pt;font-weight:600}h4 .num{margin-left:auto}
ul{list-style:none;margin:0;padding:0}li{display:flex;justify-content:space-between;gap:8pt;padding:1.5pt 0 1.5pt 12pt;color:var(--ink-2);border-bottom:.5pt dotted var(--rule)}
.keep{break-inside:avoid}
.foot{margin-top:16pt;color:var(--muted);font-size:7.5pt;border-top:.5pt solid var(--rule);padding-top:5pt}`

// RenderReport writes the monthly report as a PDF and returns its path.
func (r *Renderer) RenderReport(d *reports.MonthlyReport, outPdf string, today time.Time) (string, error) {
	if err := os.MkdirAll(filepath.Dir(outPdf), 0o755); err != nil {
		return "", Error("no report PDF was produced")
	}
	_ = os.Remove(outPdf)
	html, err := r.BuildReportHTML(d, today)
	if err != nil {
		return "", err
	}
	tmpDir, err := os.MkdirTemp("", "monthly-report-")
	if err != nil {
		return "", Error("no report PDF was produced")
	}
	defer os.RemoveAll(tmpDir)
	htmlPath := filepath.Join(tmpDir, "report.html")
	if err := os.WriteFile(htmlPath, []byte(html), 0o600); err != nil {
		return "", Error("no report PDF was produced")
	}
	if err := r.engine()(htmlPath, outPdf, reportPage); err != nil {
		if err.Error() == "the renderer timed out" {
			return "", Error("the report renderer timed out")
		}
		return "", Error("no report PDF was produced")
	}
	if _, statErr := os.Stat(outPdf); statErr != nil {
		return "", Error("no report PDF was produced")
	}
	return outPdf, nil
}
