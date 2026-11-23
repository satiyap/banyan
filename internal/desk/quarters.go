package desk

// Quarterly income and the balance it leaves, beside the register's own
// spending. Income is typed in from the bank statements, one figure a
// quarter; spending is the register's cheques by the date they were written,
// plus whatever was spent in the quarter without a cheque in the register.
// The balance runs forward from one quarter's closing balance at the bank.
//
// Members read it with the reports (/api/reports/quarters): totals only.
// Only the desk writes it.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/satiyap/banyan/internal/amount"
	"github.com/satiyap/banyan/internal/db"
	"github.com/satiyap/banyan/internal/reports"
)

var (
	quarterFigures = model(
		named{"fy", integer(intOptions{ge: bound(2000), le: bound(2100)})},
		named{"quarter", integer(intOptions{ge: bound(1), le: bound(4)})},
		named{"income", str(strOptions{max: 40})},
		named{"outside", str(strOptions{def: def(""), max: 40})},
	)
	quarterStart = model(
		named{"fy", integer(intOptions{ge: bound(2000), le: bound(2100)})},
		named{"quarter", integer(intOptions{ge: bound(1), le: bound(4)})},
		named{"closing", str(strOptions{max: 40})},
	)
)

type quarterKey struct{ fy, quarter int64 }

func (k quarterKey) next() quarterKey {
	if k.quarter == 4 {
		return quarterKey{k.fy + 1, 1}
	}
	return quarterKey{k.fy, k.quarter + 1}
}

func (k quarterKey) after(o quarterKey) bool {
	return k.fy > o.fy || (k.fy == o.fy && k.quarter > o.quarter)
}

func (k quarterKey) label() string {
	return "Q" + strconv.FormatInt(k.quarter, 10) + " " + reports.FyLabel(int(k.fy))
}

func (a *App) currentQuarter() (quarterKey, error) {
	today := a.today()
	fy, err := reports.FyOf(today)
	if err != nil {
		return quarterKey{}, err
	}
	q, err := reports.QuarterOf(today)
	if err != nil {
		return quarterKey{}, err
	}
	return quarterKey{int64(fy), int64(q)}, nil
}

// paiseOrNothing reads a typed amount where nothing, or 0, means none.
func paiseOrNothing(text string) (int64, error) {
	clean := strings.NewReplacer(",", "", "₹", "", " ", "").Replace(strings.TrimSpace(text))
	if clean == "" || strings.Trim(clean, "0.") == "" {
		return 0, nil
	}
	parsed, err := amount.ParseAmount(text)
	if err != nil {
		return 0, err
	}
	return parsed.PaiseTotal, nil
}

type quarterOut struct {
	Fy            int64  `json:"fy"`
	Quarter       int64  `json:"quarter"`
	Label         string `json:"label"`
	Current       bool   `json:"current"`
	Entered       bool   `json:"entered"`
	IncomePaise   int64  `json:"income_paise"`
	RegisterPaise int64  `json:"register_paise"`
	OutsidePaise  int64  `json:"outside_paise"`
	SpentPaise    int64  `json:"spent_paise"`
	NetPaise      int64  `json:"net_paise"`
	ClosingPaise  int64  `json:"closing_paise"`
}

type quarterStartOut struct {
	Fy           int64  `json:"fy"`
	Quarter      int64  `json:"quarter"`
	Label        string `json:"label"`
	ClosingPaise int64  `json:"closing_paise"`
}

// quarters is every quarter from the one after the starting point up to the
// current one, with its income, spending and the balance it closed on.
func (a *App) quarters(rq *req) (*response, error) {
	if err := rq.check(); err != nil {
		return nil, err
	}
	now, err := a.currentQuarter()
	if err != nil {
		return nil, err
	}
	store := db.QuartersOf(a.db)
	rows, err := store.List()
	if err != nil {
		return nil, err
	}
	typed := map[quarterKey]db.QuarterRow{}
	var start *db.QuarterRow
	for i, r := range rows {
		typed[quarterKey{r.Fy, r.Quarter}] = r
		if r.ClosingPaise != nil && start == nil {
			start = &rows[i]
		}
	}
	out := []quarterOut{}
	var startOut any
	if start != nil {
		from := quarterKey{start.Fy, start.Quarter}
		startOut = quarterStartOut{from.fy, from.quarter, from.label(), *start.ClosingPaise}
		balance := *start.ClosingPaise
		for k := from.next(); !k.after(now); k = k.next() {
			q := int(k.quarter)
			first, last, err := reports.PeriodBounds(int(k.fy), &q)
			if err != nil {
				return nil, err
			}
			written, err := store.IssuedBetween(first, last)
			if err != nil {
				return nil, err
			}
			r, entered := typed[k]
			spent := written + r.OutsidePaise
			balance += r.IncomePaise - spent
			out = append(out, quarterOut{
				Fy: k.fy, Quarter: k.quarter, Label: k.label(), Current: k == now, Entered: entered,
				IncomePaise: r.IncomePaise, RegisterPaise: written, OutsidePaise: r.OutsidePaise,
				SpentPaise: spent, NetPaise: r.IncomePaise - spent, ClosingPaise: balance,
			})
		}
	}
	return ok(obj{{"start", startOut}, {"quarters", out}, {"current", obj{{"fy", now.fy}, {"quarter", now.quarter}, {"label", now.label()}}}})
}

// The quarter being set must have happened: no figures for a quarter yet to come.
func (a *App) pastOrCurrent(k quarterKey) error {
	now, err := a.currentQuarter()
	if err != nil {
		return err
	}
	if k.after(now) {
		return refuse(http.StatusUnprocessableEntity, k.label()+" has not started yet.")
	}
	return nil
}

func (a *App) setQuarter(rq *req) (*response, error) {
	v := rq.body(quarterFigures)
	if err := rq.check(); err != nil {
		return nil, err
	}
	k := quarterKey{v.int("fy"), v.int("quarter")}
	if err := a.pastOrCurrent(k); err != nil {
		return nil, err
	}
	income, err := paiseOrNothing(v.str("income"))
	if err != nil {
		return nil, refuse(http.StatusUnprocessableEntity, "Income: "+message(err))
	}
	outside, err := paiseOrNothing(v.str("outside"))
	if err != nil {
		return nil, refuse(http.StatusUnprocessableEntity, "Spent outside the register: "+message(err))
	}
	changed, err := db.QuartersOf(a.db).SetFigures(k.fy, k.quarter, income, outside)
	if err != nil {
		return nil, err
	}
	return ok(obj{{"fy", k.fy}, {"quarter", k.quarter}, {"changed", changed}})
}

func (a *App) setQuarterStart(rq *req) (*response, error) {
	v := rq.body(quarterStart)
	if err := rq.check(); err != nil {
		return nil, err
	}
	k := quarterKey{v.int("fy"), v.int("quarter")}
	if err := a.pastOrCurrent(k); err != nil {
		return nil, err
	}
	closing, err := paiseOrNothing(v.str("closing"))
	if err != nil {
		return nil, refuse(http.StatusUnprocessableEntity, "Closing balance: "+message(err))
	}
	changed, err := db.QuartersOf(a.db).SetStart(k.fy, k.quarter, closing)
	if err != nil {
		return nil, err
	}
	return ok(obj{{"fy", k.fy}, {"quarter", k.quarter}, {"changed", changed}})
}
