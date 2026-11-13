package db

import "time"

// ChequeRow is one row of cheques.
type ChequeRow struct {
	ID          int64  `json:"id"`
	ChequeNo    string `json:"cheque_no"`
	Payee       string `json:"payee"`
	AmountPaise int64  `json:"amount_paise"`
	AmountWords string `json:"amount_words"`
	ChequeDate  string `json:"cheque_date"`
	Memo        string `json:"memo"`
	Status      string `json:"status"`
	JobID       string `json:"job_id"`
	Printer     string `json:"printer"`
	PdfPath     string `json:"pdf_path"`
	CalVersion  int64  `json:"cal_version"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	EntityID    *int64 `json:"entity_id"`
	BatchID     *int64 `json:"batch_id"`
}

// ChequeWithEntity is a cheque as GetCheque and ListCheques return it: with
// its entity's name, empty when it has none.
type ChequeWithEntity struct {
	ChequeRow
	EntityName string `json:"entity_name"`
}

// EntityRow is one row of entities.
type EntityRow struct {
	ID           int64  `json:"id"`
	PayeeName    string `json:"payee_name"`
	EntityName   string `json:"entity_name"`
	AcPayee      int64  `json:"ac_payee"`
	SealRequired int64  `json:"seal_required"`
	Category     string `json:"category"`
	Notes        string `json:"notes"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	Kind         string `json:"kind"`
	DateRule     string `json:"date_rule"`
	RemindDays   string `json:"remind_days"`
	PayeePrefix  string `json:"payee_prefix"`
	Head         string `json:"head"`
}

// EventRow is one row of events.
type EventRow struct {
	ID       int64  `json:"id"`
	ChequeID *int64 `json:"cheque_id"`
	Kind     string `json:"kind"`
	Detail   string `json:"detail"`
	At       string `json:"at"`
	Subject  string `json:"subject"`
}

// BatchItem is one drafted cheque of a batch, as the HTTP layer stores it.
type BatchItem = Fields

// BatchRow is one row of batches, with its items read from their JSON.
type BatchRow struct {
	ID        int64       `json:"id"`
	Status    string      `json:"status"`
	Items     []BatchItem `json:"items"`
	NextNo    string      `json:"next_no"`
	Printer   string      `json:"printer"`
	CreatedAt string      `json:"created_at"`
	UpdatedAt string      `json:"updated_at"`
}

// batchStored is a batch as the table holds it.
type batchStored struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"`
	Items     string `json:"items"`
	NextNo    string `json:"next_no"`
	Printer   string `json:"printer"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// BookRow is one row of cheque_books.
type BookRow struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	FirstNo    string `json:"first_no"`
	LastNo     string `json:"last_no"`
	ReceivedOn string `json:"received_on"`
	Position   int64  `json:"position"`
	AlertState string `json:"alert_state"`
	AlertUntil string `json:"alert_until"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// BlankLeafRow is one row of blank_leaves.
type BlankLeafRow struct {
	ID        int64  `json:"id"`
	ChequeNo  string `json:"cheque_no"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"created_at"`
}

// EntityMeta is what the frequency analysis needs to know of an entity.
type EntityMeta struct {
	Category   string `json:"category"`
	Notes      string `json:"notes"`
	EntityName string `json:"entity_name"`
	Kind       string `json:"kind"`
}

// NewCheque holds the columns of a new cheque, by column name. payee is
// required. one_off is not a column: it decides the kind of a new entity.
type NewCheque = Fields

// ChequeChanges holds column changes for a cheque, plus the event to record with them.
type ChequeChanges struct {
	Event   string
	Detail  string
	Columns Fields
}

// Outcome is the verdict on one queued cheque: 'printed' or 'failed'.
type Outcome struct {
	ChequeID int64
	Outcome  string
}

// ListChequesOptions filters and pages ListCheques. A zero field is not a
// filter; a nil Limit is 500.
type ListChequesOptions struct {
	Status   string `json:"status"`
	Payee    string `json:"payee"`
	Search   string `json:"search"`
	EntityID *int64 `json:"entity_id"`
	Limit    *int64 `json:"limit"`
	Offset   int64  `json:"offset"`
}

// EntityFields holds fields of an entity, by column name. Unknown names and
// nil values are ignored.
type EntityFields = Fields

// NewBook is a cheque book to shelve; Detail is the text of its event.
type NewBook struct {
	Name       string `json:"name"`
	FirstNo    string `json:"first_no"`
	LastNo     string `json:"last_no"`
	ReceivedOn string `json:"received_on"`
	Front      bool   `json:"front"`
	Detail     string `json:"detail"`
}

// BookChanges is the event to record for a book, plus its column changes;
// Front moves it to the front of the shelf.
type BookChanges struct {
	Kind    string
	Detail  string
	Front   bool
	Columns Fields
}

// BatchChanges holds column changes made with a batch's stage move; items is
// stored as JSON.
type BatchChanges = Fields

// EventOptions says what an event is about.
type EventOptions struct {
	Subject  string `json:"subject"`
	ChequeID *int64 `json:"cheque_id"`
}

// Options configures Open.
type Options struct {
	// Now is the clock. Only tests replace it.
	Now func() time.Time
}
