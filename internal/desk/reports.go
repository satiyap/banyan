package desk

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/satiyap/banyan/internal/db"
	"github.com/satiyap/banyan/internal/pycompat"
	"github.com/satiyap/banyan/internal/reports"
)

func reportRows(issued []db.ChequeRow) []reports.ReportChequeRow {
	out := make([]reports.ReportChequeRow, len(issued))
	for i, c := range issued {
		out[i] = reports.ReportChequeRow{
			ID: c.ID, ChequeNo: c.ChequeNo, Payee: c.Payee, AmountPaise: c.AmountPaise,
			ChequeDate: c.ChequeDate, Memo: c.Memo, EntityID: c.EntityID,
		}
	}
	return out
}

func reportEntity(e db.EntityRow) reports.ReportEntityRow {
	return reports.ReportEntityRow{ID: e.ID, PayeeName: e.PayeeName, EntityName: e.EntityName, Head: &e.Head, Kind: &e.Kind}
}

func reportEntities(entities []db.EntityRow) []reports.ReportEntityRow {
	out := make([]reports.ReportEntityRow, len(entities))
	for i, e := range entities {
		out[i] = reportEntity(e)
	}
	return out
}

// reportInputs is what every report is built from: the issued cheques and the entity list.
func (a *App) reportInputs() ([]reports.ReportChequeRow, []reports.ReportEntityRow, error) {
	issued, err := a.db.IssuedCheques()
	if err != nil {
		return nil, nil, err
	}
	entities, err := a.db.ListEntities()
	if err != nil {
		return nil, nil, err
	}
	return reportRows(issued), reportEntities(entities), nil
}

func (a *App) buildReport(fy, quarter *int64) (*reports.Report, error) {
	if quarter != nil && (*quarter < 1 || *quarter > 4) {
		return nil, refuse(http.StatusBadRequest, "quarter must be 1, 2, 3 or 4")
	}
	var year int64
	if fy != nil {
		year = *fy
	} else {
		current, err := reports.FyOf(a.today())
		if err != nil {
			return nil, err
		}
		year = int64(current)
	}
	if year < 2000 || year > 2100 {
		return nil, refuse(http.StatusBadRequest, "that financial year is out of range")
	}
	rows, entities, err := a.reportInputs()
	if err != nil {
		return nil, err
	}
	return reports.Build(rows, entities, int(year), intOf(quarter), a.today())
}

// buildMonthly is each month under each expense head: one financial year, or the last N months.
func (a *App) buildMonthly(fy, last *int64) (*reports.MonthlyReport, error) {
	if last != nil && (*last < 1 || *last > 24) {
		return nil, refuse(http.StatusBadRequest, "last must be between 1 and 24 months")
	}
	if fy != nil && (*fy < 2000 || *fy > 2100) {
		return nil, refuse(http.StatusBadRequest, "that financial year is out of range")
	}
	rows, entities, err := a.reportInputs()
	if err != nil {
		return nil, err
	}
	return reports.Monthly(rows, entities, intOf(fy), intOf(last), a.today())
}

func (a *App) monthlyReport(rq *req) (*response, error) {
	fy := rq.queryInt("fy", optionalInt)
	last := rq.queryInt("last", optionalInt)
	if err := rq.check(); err != nil {
		return nil, err
	}
	d, err := a.buildMonthly(fy, last)
	if err != nil {
		return nil, err
	}
	return ok(d)
}

// monthlyReportPDF is the monthly report as an A4 PDF, for a committee meeting or the file.
func (a *App) monthlyReportPDF(rq *req) (*response, error) {
	fy := rq.queryInt("fy", optionalInt)
	last := rq.queryInt("last", optionalInt)
	if err := rq.check(); err != nil {
		return nil, err
	}
	d, err := a.buildMonthly(fy, last)
	if err != nil {
		return nil, err
	}
	var period string
	if d.Last != nil && *d.Last != 0 {
		period = "last-" + strconv.Itoa(*d.Last) + "-months"
	} else {
		if d.Fy == nil {
			return nil, refuse(http.StatusInternalServerError, "the report has no financial year")
		}
		words := strings.FieldsFunc(reports.FyLabel(*d.Fy), pycompat.IsSpace)
		period = "fy-" + strings.Replace(words[len(words)-1], "–", "-", 1)
	}
	name := "banyan-monthly-" + period + ".pdf"
	folder, err := os.MkdirTemp(a.cfg.PDFDir, "banyan-report-")
	if err != nil {
		return nil, err
	}
	path, err := a.renderer.RenderReport(d, filepath.Join(folder, name), time.Now())
	if err != nil {
		if isType(err, "RenderError") {
			return nil, refuse(http.StatusInternalServerError, "the report could not be rendered: "+message(err))
		}
		return nil, err
	}
	return a.pdfResponse(path, name)
}

// report is every report for a financial year, or one quarter of it (Q1 = Apr–Jun).
func (a *App) report(rq *req) (*response, error) {
	fy := rq.queryInt("fy", optionalInt)
	quarter := rq.queryInt("quarter", optionalInt)
	if err := rq.check(); err != nil {
		return nil, err
	}
	data, err := a.buildReport(fy, quarter)
	if err != nil {
		return nil, err
	}
	return ok(data)
}

var reportCSVHeads = map[string][]string{
	"entities": {
		"Entity", "Prints as", "Kind", "Head", "Cheques", "Total (INR)", "Share %",
		"Average (INR)", "Largest (INR)", "Previous period (INR)", "Last paid",
	},
	"categories": {"Head", "Entities", "Cheques", "Total (INR)", "Share %", "Previous period (INR)"},
	"payees":     {"Payee (as printed)", "Entities", "Cheques", "Total (INR)", "Share %"},
	"cheques":    {"Cheque no", "Cheque date", "Payee", "Entity", "Head", "Amount (INR)", "Memo", "Status"},
}

func csvResponse(text, filename string) *response {
	return &response{
		status: http.StatusOK,
		header: [][2]string{
			{"Content-Type", "text/csv; charset=utf-8"},
			{"Content-Disposition", `attachment; filename="` + filename + `"`},
		},
		body: []byte(text),
	}
}

func rupees(columns []int64) []string {
	out := make([]string, len(columns))
	for i, paise := range columns {
		out[i] = rupee(paise)
	}
	return out
}

// reportCSV is one report as a spreadsheet, with its period breakdown as
// extra columns. Every text cell a person typed is neutralised against
// formula execution.
func (a *App) reportCSV(rq *req) (*response, error) {
	kind, _ := rq.path("kind", str(strOptions{})).(string)
	fy := rq.queryInt("fy", optionalInt)
	quarter := rq.queryInt("quarter", optionalInt)
	if err := rq.check(); err != nil {
		return nil, err
	}
	head, known := reportCSVHeads[kind]
	if !known {
		return nil, refuse(http.StatusNotFound, "no such report")
	}
	data, err := a.buildReport(fy, quarter)
	if err != nil {
		return nil, err
	}
	var buf strings.Builder
	pct := func(f float64) string { return fixed(f*100, 1) }
	count := strconv.Itoa

	switch kind {
	case "cheques":
		buf.WriteString(csvRow(head...))
		entities, err := a.db.ListEntities()
		if err != nil {
			return nil, err
		}
		meta := map[int64]db.EntityRow{}
		for _, e := range entities {
			meta[e.ID] = e
		}
		issued, err := a.db.IssuedCheques()
		if err != nil {
			return nil, err
		}
		for _, r := range issued {
			if !(pycompat.CmpStr(data.Start, r.ChequeDate) <= 0 && pycompat.CmpStr(r.ChequeDate, data.End) <= 0) {
				continue
			}
			entityName := ""
			var entity *reports.ReportEntityRow
			if r.EntityID != nil {
				if e, found := meta[*r.EntityID]; found {
					row := reportEntity(e)
					entity, entityName = &row, e.EntityName
				}
			}
			buf.WriteString(csvRow(
				csvSafe(r.ChequeNo), r.ChequeDate, csvSafe(r.Payee),
				csvSafe(entityName),
				csvSafe(reports.HeadOf(entity)),
				rupee(r.AmountPaise), csvSafe(r.Memo), r.Status,
			))
		}
	case "payees":
		buf.WriteString(csvRow(head...))
		for _, s := range data.Payees {
			buf.WriteString(csvRow(
				csvSafe(s.Payee), csvSafe(strings.Join(s.Entities, "; ")), count(s.Count), rupee(s.Paise), pct(s.Share),
			))
		}
	default:
		labels := make([]string, len(data.Columns))
		for i, c := range data.Columns {
			labels[i] = c.Label
		}
		buf.WriteString(csvRow(append(append([]string{}, head...), labels...)...))
		var total []string
		if kind == "entities" {
			kinds := map[string]string{"one_off": "One-off", "per_cheque": "Payee per cheque"}
			for _, s := range data.Entities {
				kindName, named := kinds[s.Kind]
				if !named {
					kindName = "Regular"
				}
				cells := []string{
					csvSafe(s.EntityName), csvSafe(s.PayeeName),
					kindName,
					csvSafe(s.Category), count(s.Count), rupee(s.Paise),
					pct(s.Share), rupee(s.AveragePaise), rupee(s.LargestPaise),
					rupee(s.PreviousPaise), s.LastDate,
				}
				buf.WriteString(csvRow(append(cells, rupees(s.Columns)...)...))
			}
			total = []string{"Total", "", "", "", count(data.Totals.Count), rupee(data.Totals.Paise)}
		} else {
			for _, s := range data.Categories {
				cells := []string{
					csvSafe(s.Category), count(s.EntityCount), count(s.Count),
					rupee(s.Paise), pct(s.Share), rupee(s.PreviousPaise),
				}
				buf.WriteString(csvRow(append(cells, rupees(s.Columns)...)...))
			}
			total = []string{"Total", count(data.Totals.EntityCount), count(data.Totals.Count), rupee(data.Totals.Paise)}
		}
		for len(total) < len(head) {
			total = append(total, "")
		}
		buf.WriteString(csvRow(append(total, rupees(data.ColumnTotals)...)...))
	}

	slug := strings.Replace(data.Label, "FY ", "FY", 1)
	slug = strings.Replace(slug, "–", "-", 1)
	slug, _, _ = strings.Cut(slug, " (")
	slug = strings.ReplaceAll(slug, " ", "-")
	return csvResponse(buf.String(), "banyan-"+kind+"-"+slug+".csv"), nil
}

// exportCSV is the whole register. Every column a person can type into is
// neutralised against spreadsheet formula execution, and quoted so a comma
// cannot shift the row.
func (a *App) exportCSV(rq *req) (*response, error) {
	rows, err := a.listEverything()
	if err != nil {
		return nil, err
	}
	var buf strings.Builder
	buf.WriteString(csvRow(
		"Cheque no", "Cheque date", "Payee", "Entity", "Amount (INR)",
		"Amount in words", "Memo", "Status", "Printer", "Calibration", "Recorded at",
	))
	for _, r := range rows {
		buf.WriteString(csvRow(
			csvSafe(r.ChequeNo), r.ChequeDate, csvSafe(r.Payee),
			csvSafe(r.EntityName),
			rupee(r.AmountPaise),
			csvSafe(r.AmountWords), csvSafe(r.Memo), r.Status,
			csvSafe(r.Printer), itoa(r.CalVersion), r.CreatedAt,
		))
	}
	return csvResponse(buf.String(), "banyan-cheque-register.csv"), nil
}

// listEverything is the whole register, newest cheque date first.
func (a *App) listEverything() ([]db.ChequeWithEntity, error) {
	all := int64(100000)
	return a.db.ListCheques(db.ListChequesOptions{Limit: &all})
}
