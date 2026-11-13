package desk

import (
	"net/http"
	"sort"

	"github.com/satiyap/banyan/internal/dating"
	"github.com/satiyap/banyan/internal/db"
	"github.com/satiyap/banyan/internal/frequency"
	"github.com/satiyap/banyan/internal/pycompat"
)

func frequencyRows(issued []db.ChequeRow) []frequency.ChequeRow {
	out := make([]frequency.ChequeRow, len(issued))
	for i, c := range issued {
		out[i] = frequency.ChequeRow{
			ID: c.ID, ChequeNo: c.ChequeNo, Payee: c.Payee, AmountPaise: c.AmountPaise,
			ChequeDate: c.ChequeDate, Memo: c.Memo, Status: c.Status, EntityID: c.EntityID,
		}
	}
	return out
}

func frequencyMeta(meta map[int64]db.EntityMeta) frequency.EntityMetaMap {
	out := frequency.EntityMetaMap{}
	for id, m := range meta {
		m := m
		out[id] = frequency.EntityMeta{Category: &m.Category, Notes: &m.Notes, EntityName: &m.EntityName, Kind: &m.Kind}
	}
	return out
}

// rhythms is the frequency analysis of the register, and its headline numbers.
func (a *App) rhythms(issued []db.ChequeRow, today string) ([]frequency.PayeeRhythmDict, frequency.FrequencySummary, error) {
	meta, err := a.db.EntityMeta()
	if err != nil {
		return nil, frequency.FrequencySummary{}, err
	}
	rows := frequencyRows(issued)
	found, err := frequency.Analyse(rows, today, frequencyMeta(meta))
	if err != nil {
		return nil, frequency.FrequencySummary{}, err
	}
	summary, err := frequency.Summarise(found, rows, today)
	if err != nil {
		return nil, frequency.FrequencySummary{}, err
	}
	dicts := make([]frequency.PayeeRhythmDict, len(found))
	for i, r := range found {
		dicts[i] = r.AsDict()
	}
	return dicts, summary, nil
}

func (a *App) frequency(rq *req) (*response, error) {
	issued, err := a.db.IssuedCheques()
	if err != nil {
		return nil, err
	}
	payees, summary, err := a.rhythms(issued, a.today())
	if err != nil {
		return nil, err
	}
	return ok(obj{{"payees", payees}, {"summary", summary}, {"today", a.today()}})
}

// entityView is an entity with what the register knows about paying it.
type entityView struct {
	ID           int64    `json:"id"`
	PayeeName    string   `json:"payee_name"`
	EntityName   string   `json:"entity_name"`
	AcPayee      bool     `json:"ac_payee"`
	SealRequired bool     `json:"seal_required"`
	Category     string   `json:"category"`
	Notes        string   `json:"notes"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	Kind         string   `json:"kind"`
	DateRule     string   `json:"date_rule"`
	RemindDays   string   `json:"remind_days"`
	PayeePrefix  string   `json:"payee_prefix"`
	Head         string   `json:"head"`
	Count        int      `json:"count"`
	TotalPaise   int64    `json:"total_paise"`
	LastDate     string   `json:"last_date"`
	RuleDate     string   `json:"rule_date"`
	Payees       []string `json:"payees"`
}

// listEntities is every entity, with what the register knows about paying it.
func (a *App) listEntities() ([]entityView, error) {
	rows, err := a.db.ListEntities()
	if err != nil {
		return nil, err
	}
	issued, err := a.db.IssuedCheques()
	if err != nil {
		return nil, err
	}

	type stat struct {
		count      int
		totalPaise int64
		lastDate   string
	}
	stats := map[int64]*stat{}
	payees := map[int64][]string{}
	for i := len(issued) - 1; i >= 0; i-- {
		c := issued[i]
		if c.EntityID == nil {
			continue
		}
		if !contains(payees[*c.EntityID], c.Payee) {
			payees[*c.EntityID] = append(payees[*c.EntityID], c.Payee)
		}
	}
	for _, c := range issued {
		if c.EntityID == nil {
			continue
		}
		s := stats[*c.EntityID]
		if s == nil {
			s = &stat{}
			stats[*c.EntityID] = s
		}
		s.count++
		s.totalPaise += c.AmountPaise
		if pycompat.CmpStr(c.ChequeDate, s.lastDate) > 0 {
			s.lastDate = c.ChequeDate
		}
	}

	now := a.today()
	out := make([]entityView, 0, len(rows))
	for _, row := range rows {
		ruleDate, err := dating.RuleDate(row.DateRule, now)
		if err != nil {
			return nil, err
		}
		view := entityView{
			ID: row.ID, PayeeName: row.PayeeName, EntityName: row.EntityName,
			AcPayee: row.AcPayee != 0, SealRequired: row.SealRequired != 0,
			Category: row.Category, Notes: row.Notes, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
			Kind: row.Kind, DateRule: row.DateRule, RemindDays: row.RemindDays,
			PayeePrefix: row.PayeePrefix, Head: row.Head,
			RuleDate: ruleDate,
			Payees:   []string{},
		}
		if s := stats[row.ID]; s != nil {
			view.Count, view.TotalPaise, view.LastDate = s.count, s.totalPaise, s.lastDate
		}
		// A payee-per-cheque entity's recent payees, newest first, offered as
		// suggestions when its next cheque is written.
		if row.Kind == db.PerCheque {
			recent := payees[row.ID]
			if len(recent) > 8 {
				recent = recent[:8]
			}
			view.Payees = append(view.Payees, recent...)
		}
		out = append(out, view)
	}
	return out, nil
}

func (a *App) getEntities(rq *req) (*response, error) {
	entities, err := a.listEntities()
	if err != nil {
		return nil, err
	}
	return ok(obj{{"entities", entities}})
}

func flag(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// orNil is a nullable text field as the register is given it.
func orNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func entityFields(req vals) db.EntityFields {
	return db.EntityFields{
		db.F("entity_name", req.str("entity_name")),
		db.F("ac_payee", flag(req.bool("ac_payee"))),
		db.F("seal_required", flag(req.bool("seal_required"))),
		db.F("category", req.str("category")),
		db.F("notes", req.str("notes")),
		db.F("kind", req.str("kind")),
		db.F("date_rule", orNil(req.strp("date_rule"))),
		db.F("remind_days", orNil(req.strp("remind_days"))),
		db.F("payee_prefix", orNil(req.strp("payee_prefix"))),
		db.F("head", orNil(req.strp("head"))),
	}
}

func (a *App) createEntity(rq *req) (*response, error) {
	req := rq.body(entityInputModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	entityID, err := a.db.CreateEntity(req.str("payee_name"), entityFields(req))
	if err != nil {
		if isType(err, "ValueError") {
			return nil, refuse(http.StatusConflict, message(err))
		}
		return nil, err
	}
	return jsonResponse(http.StatusCreated, obj{{"id", entityID}})
}

func (a *App) updateEntity(rq *req) (*response, error) {
	entityID := rq.id("entity_id")
	req := rq.body(entityInputModel)
	if err := rq.check(); err != nil {
		return nil, err
	}
	fields := append(db.EntityFields{db.F("payee_name", req.str("payee_name"))}, entityFields(req)...)
	renamed, err := a.db.UpdateEntity(entityID, fields)
	if err != nil {
		switch {
		case db.IsA(err, "LookupError"):
			return nil, refuse(http.StatusNotFound, message(err))
		case isType(err, "ValueError"):
			return nil, refuse(http.StatusConflict, message(err))
		}
		return nil, err
	}
	return ok(obj{{"id", entityID}, {"renamed", renamed}})
}

func (a *App) deleteEntity(rq *req) (*response, error) {
	entityID := rq.id("entity_id")
	if err := rq.check(); err != nil {
		return nil, err
	}
	deleted, err := a.db.DeleteEntity(entityID)
	if err != nil {
		return nil, err
	}
	if !deleted {
		return nil, refuse(http.StatusConflict, "this entity has cheques in the register, so it cannot be removed")
	}
	return ok(obj{{"deleted", entityID}})
}

// entityHistory is the last cheques issued to one entity, oldest first, and their median.
func (a *App) entityHistory(rq *req) (*response, error) {
	entityID := rq.id("entity_id")
	limit := rq.queryInt("limit", integer(intOptions{def: def(int64(8)), ge: bound(1), le: bound(60)}))
	if err := rq.check(); err != nil {
		return nil, err
	}
	entity, err := a.db.GetEntity(entityID)
	if err != nil {
		return nil, err
	}
	if entity == nil {
		return nil, refuse(http.StatusNotFound, "no such entity")
	}
	most := int64(500)
	all, err := a.db.ListCheques(db.ListChequesOptions{EntityID: &entityID, Limit: &most})
	if err != nil {
		return nil, err
	}
	rows := []chequeDict{}
	for _, r := range all {
		if int64(len(rows)) < *limit && contains(db.Issued, r.Status) {
			rows = append(rows, rowToDict(r))
		}
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	amounts := make([]int64, len(rows))
	for i, r := range rows {
		amounts[i] = r.AmountPaise
	}
	sort.Slice(amounts, func(i, j int) bool { return amounts[i] < amounts[j] })
	var median int64
	if n := len(amounts); n > 0 {
		mid := n / 2
		if n%2 == 1 {
			median = amounts[mid]
		} else {
			median = int64(pycompat.FloorDiv(int(amounts[mid-1]+amounts[mid]), 2))
		}
	}
	return ok(obj{{"cheques", rows}, {"median_paise", median}})
}
