package db

import (
	"fmt"
	"strconv"

	"github.com/satiyap/banyan/internal/amount"
)

// quartersTable is created the first time a quarter's figures are saved, not
// when the register opens: a register that never uses it opens unchanged, as
// every earlier desk's does.
const quartersTable = `-- Money that does not pass through the register, one row per financial-year
-- quarter (fy is the start year: 2026 is April 2026 to March 2027). The
-- treasurer types these in from the bank statements.
--
-- income_paise is the maintenance received in the quarter. outside_paise is
-- what was spent in the quarter without a cheque in the register: the months
-- before the register was kept, chiefly. Spending in a quarter is the
-- register's own cheques by cheque date, plus outside_paise.
--
-- closing_paise is set on one row only, the starting point: that quarter's
-- closing balance as the bank showed it. Every later quarter's balance is
-- worked out from it. Nothing here ever changes a cheque.
CREATE TABLE IF NOT EXISTS quarters (
    fy            INTEGER NOT NULL,
    quarter       INTEGER NOT NULL CHECK (quarter BETWEEN 1 AND 4),
    income_paise  INTEGER NOT NULL DEFAULT 0,
    outside_paise INTEGER NOT NULL DEFAULT 0,
    closing_paise INTEGER,
    updated_at    TEXT    NOT NULL,
    PRIMARY KEY (fy, quarter)
);
`

// QuarterRow is one quarter's typed-in figures.
type QuarterRow struct {
	Fy           int64  `json:"fy"`
	Quarter      int64  `json:"quarter"`
	IncomePaise  int64  `json:"income_paise"`
	OutsidePaise int64  `json:"outside_paise"`
	ClosingPaise *int64 `json:"closing_paise"`
	UpdatedAt    string `json:"updated_at"`
}

// Quarters is the register's quarterly income and spending outside the
// register. A type of its own, like Annotations: the Database methods are
// the register's original operations, held in place by the recordings.
type Quarters struct{ d *Database }

// QuartersOf is the quarterly figures kept in a register.
func QuartersOf(d *Database) *Quarters { return &Quarters{d} }

// QuarterSubject is what a quarter's events are about: 'quarter:2026-Q1'.
func QuarterSubject(fy, quarter int64) string {
	return "quarter:" + strconv.FormatInt(fy, 10) + "-Q" + strconv.FormatInt(quarter, 10)
}

func hasQuarters(con *conn) (bool, error) {
	row, err := con.get("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'quarters'")
	return row != nil, err
}

// ensure creates the table the first time it is needed, taking a copy of the
// register beside it first: this file is the only record of which leaves
// were used.
func (q *Quarters) ensure() error {
	has, err := connect(q.d, hasQuarters)
	if err != nil || has {
		return err
	}
	backup := q.d.backupPath("pre-quarters")
	if !exists(backup) {
		if err := q.d.do(func(con *conn) error { return q.d.backupTo(con, backup) }); err != nil {
			return err
		}
	}
	return q.d.do(func(con *conn) error { return con.exec(quartersTable) })
}

// List is every quarter's figures, oldest first. A register without the
// table has none, and reading never creates it.
func (q *Quarters) List() ([]QuarterRow, error) {
	return connect(q.d, func(con *conn) ([]QuarterRow, error) {
		if has, err := hasQuarters(con); err != nil || !has {
			return []QuarterRow{}, err
		}
		return list[QuarterRow](con, "SELECT * FROM quarters ORDER BY fy, quarter")
	})
}

// IssuedBetween is the total of cheques written from start to end inclusive
// (ISO dates), by cheque date: what the register says was spent then.
// Misprinted and void cheques are not spending.
func (q *Quarters) IssuedBetween(start, end string) (int64, error) {
	return connect(q.d, func(con *conn) (int64, error) {
		row, err := con.get(
			"SELECT COALESCE(SUM(amount_paise), 0) FROM cheques WHERE status IN ("+marks(len(Issued), ",")+") "+
				"AND cheque_date >= ? AND cheque_date <= ?",
			append(anys(Issued), start, end)...,
		)
		if err != nil || row == nil {
			return 0, err
		}
		total, _ := row[0].(int64)
		return total, nil
	})
}

// SetFigures records a quarter's income and its spending outside the
// register, with an event when either changed. changed is false when both
// were already these.
func (q *Quarters) SetFigures(fy, quarter, incomePaise, outsidePaise int64) (changed bool, err error) {
	if err := q.ensure(); err != nil {
		return false, err
	}
	return connect(q.d, func(con *conn) (bool, error) {
		before, err := one[QuarterRow](con, "SELECT * FROM quarters WHERE fy = ? AND quarter = ?", fy, quarter)
		if err != nil {
			return false, err
		}
		var was QuarterRow
		if before != nil {
			was = *before
		}
		detail := ""
		note := func(what string, from, to int64) {
			if from == to {
				return
			}
			if detail != "" {
				detail += "; "
			}
			detail += what + ": " + money(from) + " → " + money(to)
		}
		note("income", was.IncomePaise, incomePaise)
		note("spent outside the register", was.OutsidePaise, outsidePaise)
		if before != nil && detail == "" {
			return false, nil
		}
		now := q.d.utcnow()
		if _, err := con.run(
			"INSERT INTO quarters (fy, quarter, income_paise, outside_paise, updated_at) VALUES (?,?,?,?,?) "+
				"ON CONFLICT (fy, quarter) DO UPDATE SET income_paise = excluded.income_paise, "+
				"outside_paise = excluded.outside_paise, updated_at = excluded.updated_at",
			fy, quarter, incomePaise, outsidePaise, now,
		); err != nil {
			return false, err
		}
		if detail == "" {
			detail = "income: " + money(incomePaise) + "; spent outside the register: " + money(outsidePaise)
		}
		_, err = con.run("INSERT INTO events (cheque_id, kind, detail, at, subject) VALUES (?,?,?,?,?)",
			nil, "quarter-set", detail, now, QuarterSubject(fy, quarter))
		return err == nil, err
	})
}

// SetStart makes a quarter the starting point, with the closing balance the
// bank showed for it. Any earlier starting point stops being one, in the
// same statement as this one becomes it.
func (q *Quarters) SetStart(fy, quarter, closingPaise int64) (changed bool, err error) {
	if err := q.ensure(); err != nil {
		return false, err
	}
	return connect(q.d, func(con *conn) (bool, error) {
		was, err := one[QuarterRow](con, "SELECT * FROM quarters WHERE closing_paise IS NOT NULL ORDER BY fy, quarter")
		if err != nil {
			return false, err
		}
		if was != nil && was.Fy == fy && was.Quarter == quarter && *was.ClosingPaise == closingPaise {
			return false, nil
		}
		now := q.d.utcnow()
		if _, err := con.run(
			"UPDATE quarters SET closing_paise = CASE WHEN fy = ? AND quarter = ? THEN ? ELSE NULL END, "+
				"updated_at = CASE WHEN closing_paise IS NOT NULL OR (fy = ? AND quarter = ?) THEN ? ELSE updated_at END",
			fy, quarter, closingPaise, fy, quarter, now,
		); err != nil {
			return false, err
		}
		if _, err := con.run(
			"INSERT INTO quarters (fy, quarter, closing_paise, updated_at) VALUES (?,?,?,?) "+
				"ON CONFLICT (fy, quarter) DO NOTHING",
			fy, quarter, closingPaise, now,
		); err != nil {
			return false, err
		}
		detail := "starting balance: " + money(closingPaise)
		if was != nil {
			from := QuarterSubject(was.Fy, was.Quarter)
			if from == QuarterSubject(fy, quarter) {
				detail = "starting balance: " + money(*was.ClosingPaise) + " → " + money(closingPaise)
			} else {
				detail += " (was " + from[len("quarter:"):] + ", " + money(*was.ClosingPaise) + ")"
			}
		}
		_, err = con.run("INSERT INTO events (cheque_id, kind, detail, at, subject) VALUES (?,?,?,?,?)",
			nil, "quarter-start", detail, now, QuarterSubject(fy, quarter))
		return err == nil, err
	})
}

func money(paise int64) string {
	if paise < 0 {
		return "-" + money(-paise)
	}
	text := "₹" + amount.GroupIndian(paise/100)
	if paise%100 != 0 {
		text += fmt.Sprintf(".%02d", paise%100)
	}
	return text
}

// fundsTable is created the first time an accumulated fund is saved, like
// the quarters table.
const fundsTable = `-- The accumulated fund at 31 March of each financial year (fy is the start
-- year: 2025 is the fund at 31 March 2026), typed in from the audited
-- balance sheet. The year's income and expense are measured against it: a
-- year whose expense runs over its income is eating into it.
CREATE TABLE IF NOT EXISTS funds (
    fy         INTEGER PRIMARY KEY,
    fund_paise INTEGER NOT NULL,
    updated_at TEXT    NOT NULL
);
`

// FundRow is one year's accumulated fund at 31 March.
type FundRow struct {
	Fy        int64  `json:"fy"`
	FundPaise int64  `json:"fund_paise"`
	UpdatedAt string `json:"updated_at"`
}

// FundSubject is what a year's fund events are about: 'fund:2025'.
func FundSubject(fy int64) string { return "fund:" + strconv.FormatInt(fy, 10) }

func hasFunds(con *conn) (bool, error) {
	row, err := con.get("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'funds'")
	return row != nil, err
}

// Funds is every year's accumulated fund, oldest first. Reading never creates
// the table.
func (q *Quarters) Funds() ([]FundRow, error) {
	return connect(q.d, func(con *conn) ([]FundRow, error) {
		if has, err := hasFunds(con); err != nil || !has {
			return []FundRow{}, err
		}
		return list[FundRow](con, "SELECT * FROM funds ORDER BY fy")
	})
}

// SetFund records a year's accumulated fund, with an event when it changed.
// The first time, a copy of the register is taken beside it.
func (q *Quarters) SetFund(fy, fundPaise int64) (changed bool, err error) {
	has, err := connect(q.d, hasFunds)
	if err != nil {
		return false, err
	}
	if !has {
		backup := q.d.backupPath("pre-funds")
		if !exists(backup) {
			if err := q.d.do(func(con *conn) error { return q.d.backupTo(con, backup) }); err != nil {
				return false, err
			}
		}
		if err := q.d.do(func(con *conn) error { return con.exec(fundsTable) }); err != nil {
			return false, err
		}
	}
	return connect(q.d, func(con *conn) (bool, error) {
		was, err := one[FundRow](con, "SELECT * FROM funds WHERE fy = ?", fy)
		if err != nil {
			return false, err
		}
		if was != nil && was.FundPaise == fundPaise {
			return false, nil
		}
		now := q.d.utcnow()
		if _, err := con.run(
			"INSERT INTO funds (fy, fund_paise, updated_at) VALUES (?,?,?) "+
				"ON CONFLICT (fy) DO UPDATE SET fund_paise = excluded.fund_paise, updated_at = excluded.updated_at",
			fy, fundPaise, now,
		); err != nil {
			return false, err
		}
		detail := "accumulated fund at 31 March " + strconv.FormatInt(fy+1, 10) + ": " + money(fundPaise)
		if was != nil {
			detail = "accumulated fund at 31 March " + strconv.FormatInt(fy+1, 10) + ": " + money(was.FundPaise) + " → " + money(fundPaise)
		}
		_, err = con.run("INSERT INTO events (cheque_id, kind, detail, at, subject) VALUES (?,?,?,?,?)",
			nil, "fund-set", detail, now, FundSubject(fy))
		return err == nil, err
	})
}
