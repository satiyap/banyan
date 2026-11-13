package desk

// Request and response shapes for the API. The fields of each model are
// checked in the order they are listed here, which is the order Pydantic
// reported them in.

const dateRule = `^(today|(next|this):([1-9]|[12]\d|3[01]))$`
const remindDays = `^(\d{1,2}(,\d{1,2}){0,3})?$`

func chequeInputFields() []named {
	return []named{
		{"payee", str(strOptions{min: 1, max: 120, after: []func(string) (string, error){tidied, payeeNotBlank}})},
		// Which entity this payment is for. Needed only when several entities
		// print the same payee name; otherwise the name alone decides.
		{"entity_id", integer(intOptions{def: def(nil), nullable: true})},
		// When the payee is new: record them as a one-off rather than a regular.
		{"one_off", boolean(def(false))},
		{"amount", str(strOptions{min: 1, max: 32})},
		{"cheque_date", date(dateOptions{})},
		// Collapse whitespace so "Sunrise   Services" and "Sunrise Services"
		// are one payee in the frequency analysis rather than two.
		{"cheque_no", str(strOptions{def: def(""), max: 40, after: []func(string) (string, error){tidied}})},
		{"memo", str(strOptions{def: def(""), max: 200, after: []func(string) (string, error){tidied}})},
		{"crossing", boolean(def(true))},
		{"strike_order", boolean(def(false))},
		{"star_fill", boolean(def(false))},
	}
}

func withCheque(more ...named) field {
	return model(append(chequeInputFields(), more...)...)
}

var tidyOnly = []func(string) (string, error){tidied}

var (
	// What the treasurer types in to write one cheque.
	chequeInputModel = model(chequeInputFields()...)

	// A cheque to print, or to record as already written.
	issueRequestModel = withCheque(
		named{"action", literal([]string{"print", "log"}, literalOptions{def: def("print")})},
		named{"printer", str(strOptions{def: def("")})},
		named{"confirm", boolean(def(false))}, // printing a real leaf requires an explicit yes
	)

	confirmRequestModel = model(
		named{"outcome", literal([]string{"printed", "failed"}, literalOptions{})},
		named{"detail", str(strOptions{def: def(""), max: 200})},
	)

	voidRequestModel = model(
		named{"reason", str(strOptions{def: def(""), max: 200})},
	)

	// A correction to a register entry.
	//
	// Only fields that are bookkeeping rather than payment. The payee, amount,
	// date and status describe what was actually issued — changing those after
	// the fact would make the register disagree with the leaf in someone's
	// hand, so they are corrected by voiding and re-issuing instead.
	chequeEditRequestModel = model(
		named{"cheque_no", str(strOptions{def: def(nil), nullable: true, max: 40, after: tidyOnly})},
		named{"memo", str(strOptions{def: def(nil), nullable: true, max: 200, after: tidyOnly})},
		// Moving a cheque between entities that print the same name: the leaf
		// is unchanged, only which of them the desk counts it against.
		named{"entity_id", integer(intOptions{def: def(nil), nullable: true})},
	)

	// An organisation or person the association pays.
	//
	// Only payee_name is ever printed. The rest is the desk's own record of
	// how this entity is normally paid.
	entityInputModel = model(
		named{"payee_name", str(strOptions{min: 1, max: 120, after: []func(string) (string, error){tidied, payeeNameNotBlank}})},
		named{"entity_name", str(strOptions{def: def(""), max: 100, after: tidyOnly})},
		named{"ac_payee", boolean(def(true))},
		named{"seal_required", boolean(def(false))},
		named{"category", str(strOptions{def: def(""), max: 60, after: tidyOnly})},
		named{"notes", str(strOptions{def: def(""), max: 500})},
		// The report head. Left out, kept: a page from before heads were fixed.
		named{"head", literal([]string{"", "Salary", "Utilities", "AMC", "Misc", "GST / Tax"}, literalOptions{def: def(nil), nullable: true})},
		// 'per_cheque': the payee is typed on each cheque (GST to a fresh CRN),
		// so payee_name is the entity's own label and never reaches a leaf.
		named{"kind", literal([]string{"regular", "one_off", "per_cheque"}, literalOptions{def: def("regular")})},
		// What a per_cheque entity's payee starts from in the Tray. Left out, kept.
		named{"payee_prefix", str(strOptions{def: def(nil), nullable: true, max: 60, after: tidyOnly})},
		// Left out, these keep what the entity has: a page from before they
		// existed must not reset them by saving an unrelated edit.
		named{"date_rule", str(strOptions{def: def(nil), nullable: true, pattern: unicodeDigits(dateRule)})},
		named{"remind_days", str(strOptions{def: def(nil), nullable: true, pattern: unicodeDigits(remindDays)})},
	)

	testPrintRequestModel = model(
		named{"printer", str(strOptions{def: def("")})},
		named{"confirm", boolean(def(false))},
		named{"pattern", literal([]string{"test", "blank"}, literalOptions{def: def("test")})},
	)

	// A run of cheques to review and then print, first one alone.
	//
	// Each item's own cheque_no is ignored: leaves are numbered in the order
	// they feed, from first_cheque_no, and only when they are actually printed
	// — so a misprinted first leaf moves every later number on by one, as it should.
	batchCreateModel = model(
		named{"items", list(chequeInputModel, listOptions{min: 1, max: 50})},
		named{"first_cheque_no", str(strOptions{min: 1, max: 40, after: []func(string) (string, error){numbered}})},
		named{"printer", str(strOptions{def: def("")})},
	)

	batchStepModel = model(
		named{"confirm", boolean(def(false))}, // sending leaves to the printer needs an explicit yes
	)

	batchFirstOutcomeModel = model(
		named{"outcome", literal([]string{"printed", "failed"}, literalOptions{})},
		named{"detail", str(strOptions{def: def(""), max: 200})},
	)

	// The verdict on everything after the first leaf. Empty means every leaf
	// printed correctly — the one-click case.
	batchRestOutcomeModel = model(
		named{"misprinted", list(integer(intOptions{}), listOptions{max: 50, optional: true})},
		named{"detail", str(strOptions{def: def(""), max: 200})},
	)

	// One cheque on plain paper: the real render and printer settings, but no
	// leaf number and nothing in the register.
	chequeTestPrintModel = withCheque(
		named{"printer", str(strOptions{def: def("")})},
		named{"confirm", boolean(def(false))},
	)

	batchTestPrintModel = model(
		named{"which", literal([]string{"first", "all"}, literalOptions{def: def("first")})},
		named{"confirm", boolean(def(false))},
	)

	// A cheque book, by the numbers printed on its first and last leaves.
	bookInputModel = model(
		named{"name", str(strOptions{min: 1, max: 40, after: tidyOnly})},
		named{"first_no", str(strOptions{min: 1, max: 40, after: tidyOnly})},
		named{"last_no", str(strOptions{min: 1, max: 40, after: tidyOnly})},
		named{"received_on", date(dateOptions{def: def(nil), nullable: true})},
		// 'after': behind every book already on the shelf. 'now': in front of
		// them, so it is used next even if another book has leaves left.
		named{"start", literal([]string{"after", "now"}, literalOptions{def: def("after")})},
		// A book some of whose leaves are already in the register: the one in
		// use when books were first set up. Without this, an overlap is refused.
		named{"already_used", boolean(def(false))},
	)

	bookUpdateModel = model(
		named{"name", str(strOptions{def: def(nil), nullable: true, min: 1, max: 40})},
		named{"use_now", boolean(def(false))},
	)

	bookAlertAnswerModel = model(
		named{"answer", literal([]string{"ordered", "snooze"}, literalOptions{})},
		named{"days", integer(intOptions{def: def(int64(3)), ge: bound(1), le: bound(30)})},
	)

	remindAnswerModel = model(
		named{"answer", literal([]string{"ack", "snooze"}, literalOptions{})},
		named{"days", integer(intOptions{def: def(int64(1)), ge: bound(1), le: bound(30)})},
	)

	// One leaf the register has no record of: written by hand (with what was
	// written on it) or used up blank.
	accountForItemModel = model(
		named{"cheque_no", str(strOptions{min: 1, max: 40, after: tidyOnly})},
		named{"as", literal([]string{"logged", "void"}, literalOptions{})},
		named{"reason", str(strOptions{def: def(""), max: 200})},
		named{"payee", str(strOptions{def: def(""), max: 120, after: tidyOnly})},
		named{"entity_id", integer(intOptions{def: def(nil), nullable: true})},
		named{"amount", str(strOptions{def: def(""), max: 32})},
		named{"cheque_date", date(dateOptions{def: def(nil), nullable: true})},
		named{"memo", str(strOptions{def: def(""), max: 200, after: tidyOnly})},
	)

	accountForModel = model(
		named{"items", list(accountForItemModel, listOptions{min: 1, max: 50})},
	)

	// Count register entries against another entity. The leaves are unchanged.
	moveChequesRequestModel = model(
		named{"cheque_ids", list(integer(intOptions{}), listOptions{min: 1, max: 500})},
		named{"entity_id", integer(intOptions{})},
	)
)

// chequeInput is a validated cheque as the handlers read it.
type chequeInput struct {
	Payee       string
	EntityID    *int64
	OneOff      bool
	Amount      string
	ChequeDate  string
	ChequeNo    string
	Memo        string
	Crossing    bool
	StrikeOrder bool
	StarFill    bool
}

func chequeInputOf(v vals) chequeInput {
	return chequeInput{
		Payee:       v.str("payee"),
		EntityID:    v.intp("entity_id"),
		OneOff:      v.bool("one_off"),
		Amount:      v.str("amount"),
		ChequeDate:  v.str("cheque_date"),
		ChequeNo:    v.str("cheque_no"),
		Memo:        v.str("memo"),
		Crossing:    v.bool("crossing"),
		StrikeOrder: v.bool("strike_order"),
		StarFill:    v.bool("star_fill"),
	}
}

// chequePayload is the shape the renderer and the browser preview both consume.
type chequePayload struct {
	Payee         string `json:"payee"`
	ChequeDate    string `json:"cheque_date"`
	AmountWords   string `json:"amount_words"`
	AmountFigures string `json:"amount_figures"`
	Crossing      bool   `json:"crossing"`
	StrikeOrder   bool   `json:"strike_order"`
	StarFill      bool   `json:"star_fill"`
	TestPattern   bool   `json:"test_pattern"`
}

// composeResponse is what /api/compose answers. Every key is always present, in this order.
type composeResponse struct {
	OK            bool     `json:"ok"`
	AmountPaise   int64    `json:"amount_paise"`
	AmountPlain   string   `json:"amount_plain"`
	AmountWords   string   `json:"amount_words"`
	AmountFigures string   `json:"amount_figures"`
	DateDigits    string   `json:"date_digits"`
	Warnings      []string `json:"warnings"`
	Error         string   `json:"error"`
	// A chequePayload, or an empty object when the amount was refused.
	Cheque any `json:"cheque"`
}
