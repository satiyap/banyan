package desk

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/satiyap/banyan/internal/connection"
	"github.com/satiyap/banyan/internal/desksync"
)

// Revision is one copy of the register the server has accepted.
type Revision struct {
	Revision int64  `json:"revision"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
	SyncedAt string `json:"synced_at"`
}

// SyncStatus is how the desk's register stands against the server's.
type SyncStatus struct {
	Connected bool `json:"connected"`
	// True for a reports-only app: connected, but with no register of its own to keep level.
	ReadOnly bool   `json:"read_only"`
	APIURL   string `json:"api_url"`
	// The last time the server took this desk's register, or the desk took the server's.
	SyncedAt string `json:"synced_at"`
	Revision *int64 `json:"revision"`
	// True while this desk holds changes the server has not accepted.
	Pending bool `json:"pending"`
	// Why the last attempt failed, in words for the treasurer. Empty when it worked.
	Error string `json:"error"`
	// Set when the server holds a register this desk did not start from. The treasurer must choose.
	Choice *Revision `json:"choice"`
	// True when the server says another desk has taken over. This desk can no longer send.
	Retired bool `json:"retired"`
}

// ServerInfo is what a Banyan server says about itself, with its address as it will be stored.
type ServerInfo struct {
	Name   string
	APIURL string
	// Whether the server holds a register, and its latest copy. Nil from a
	// server too old to say.
	Register *HeldRegister
}

// HeldRegister is whether a server holds a register, and the number of its latest copy.
type HeldRegister struct {
	Held     bool
	Revision *int64
}

// Syncer keeps the Banyan server's copy of the register level with this
// desk's. A refusal meant for the treasurer is a *connection.ConnectionError.
type Syncer interface {
	// ReadOnly is true when this app was connected with a read key: it shows reports and nothing else.
	ReadOnly() bool
	Status() SyncStatus
	// Discover asks an address whether it is a Banyan server this desk can work with.
	Discover(apiURL any) (ServerInfo, error)
	// ConnectAs redeems a key for this desk and remembers the token it buys.
	// want ("write", "read" or "") is the role the key must have: a key of the
	// other role is refused and not used up. Without flush nothing is sent yet.
	ConnectAs(apiURL, userKey any, want string, flush bool) error
	// Agreement is the number of the last copy this desk and the server at
	// apiURL agreed on.
	Agreement(apiURL string) (int64, bool)
	// EverAgreed is true when this desk's register is a copy some server already holds.
	EverAgreed() bool
	// Disconnect forgets the key, and the server's address too when asked.
	Disconnect(forgetServer bool) error
	// Schedule notes that the register may have changed, and sends it shortly.
	Schedule()
	// Flush sends the register now if the server does not have it, and waits for the answer.
	Flush() error
	// FetchForRestore fetches the server's register and leaves it beside this
	// desk's, to be put in place when the app restarts.
	FetchForRestore() error
	// Read fetches something a reader may see from the Banyan server, as that
	// reader. The caller closes the body.
	Read(path string) (*http.Response, error)
	// Send makes one call to the Banyan server as this app, with a JSON body
	// when one is given. The caller closes the body.
	Send(method, path string, body []byte) (*http.Response, error)
}

// offline is the Syncer of a desk with no way to reach a server: it remembers
// that the question was skipped and does nothing else.
type offline struct {
	connectionPath string
}

// NoSync is a Syncer that never talks to a server. Skipping the connection
// question is still remembered in the connection file, when a path is given.
func NoSync(connectionPath string) Syncer { return offline{connectionPath: connectionPath} }

func notConnected(what string) error {
	return &connection.ConnectionError{Message: what}
}

func (offline) ReadOnly() bool     { return false }
func (offline) Status() SyncStatus { return SyncStatus{} }
func (offline) Schedule()          {}
func (offline) Flush() error       { return nil }
func (offline) EverAgreed() bool   { return false }

func (offline) Agreement(string) (int64, bool) { return 0, false }

const noServer = "Connecting to a Banyan server is not available in this build."

func (offline) Discover(any) (ServerInfo, error)       { return ServerInfo{}, notConnected(noServer) }
func (offline) ConnectAs(any, any, string, bool) error { return notConnected(noServer) }

func (o offline) Disconnect(forgetServer bool) error {
	if o.connectionPath == "" {
		return nil
	}
	known := connection.ReadConnection(o.connectionPath)
	var remember *connection.Remembered
	if !forgetServer && known.Status != connection.StatusUnset && known.ApiUrl != "" {
		remember = &connection.Remembered{ApiUrl: known.ApiUrl, ServerName: known.ServerName}
	}
	_, err := connection.SkipConnection(o.connectionPath, remember)
	return err
}

func (offline) FetchForRestore() error {
	return notConnected("This desk is not connected to the server.")
}

func (offline) Read(string) (*http.Response, error) {
	return nil, notConnected("This app is not connected to the server.")
}

func (offline) Send(string, string, []byte) (*http.Response, error) {
	return nil, notConnected("This app is not connected to the server.")
}

// ------------------------------------------------------- server connection
// The desktop app asks once, on first launch, for the API URL and user key of
// the Banyan server, and can be told to skip.

type connectionNow struct {
	connection.Description
	Sync        SyncStatus `json:"sync"`
	DeskVersion string     `json:"desk_version"`
	// Registers set aside when this Mac was switched to a read key, offered
	// back only while the register in use has no leaves.
	SetAside []desksync.SetAside `json:"set_aside"`
}

func (a *App) connectionNow() connectionNow {
	now := connectionNow{
		Description: connection.DescribeConnection(connection.ReadConnection(a.cfg.ConnectionPath)),
		Sync:        a.syncer.Status(),
		DeskVersion: a.version,
		SetAside:    []desksync.SetAside{},
	}
	if !a.syncer.ReadOnly() {
		if leaves, err := a.db.UsedNumbers(); err == nil && len(leaves) == 0 {
			now.SetAside = append(now.SetAside, desksync.ListSetAside(a.cfg.DBPath)...)
		}
	}
	return now
}

// withServer runs something that talks to the Banyan server, answering its refusals in words.
func (a *App) withServer(work func() error) (*response, error) {
	if err := work(); err != nil {
		var refused *connection.ConnectionError
		if errors.As(err, &refused) {
			return nil, refuse(http.StatusBadRequest, refused.Message)
		}
		return nil, err
	}
	return ok(a.connectionNow())
}

func (a *App) getConnection(rq *req) (*response, error) { return ok(a.connectionNow()) }

// ---------------------------------------------------------- adding a key
//
// A key says what it is for in its first letter (W- writes, R- reads), so the
// app can say what connecting will do before the key is spent:
//
//   write key, server holds nothing       → this Mac's register seeds it
//   write key, server holds this Mac's    → carry on syncing
//   write key, server holds another       → only with confirmation: this Mac's
//                                           register is set aside and the
//                                           server's is used, and the leaves
//                                           this Mac used are recorded in it
//   read key, this Mac holds records      → only with confirmation: this Mac's
//                                           register is set aside, and the app
//                                           shows the server's reports
//
// The desk never replaces a register the server holds unless it started from
// that same copy. A key with no prefix, or a server too old to say what it
// holds, connects as before.

// KeyRole is the role a key's prefix names: "write", "read", or "" for an
// older key that does not say.
func KeyRole(key string) string {
	k := strings.ToUpper(strings.TrimSpace(key))
	switch {
	case strings.HasPrefix(k, "W-"):
		return "write"
	case strings.HasPrefix(k, "R-"):
		return "read"
	}
	return ""
}

type connectionPlan struct {
	Role   string `json:"role"`
	Action string `json:"action"` // "connect", "replace" or "set-aside"
	Server obj    `json:"server"`
	Local  obj    `json:"local"`
}

func (a *App) plan(apiURL, userKey any) (connectionPlan, error) {
	key, err := connection.CleanUserKey(userKey)
	if err != nil {
		return connectionPlan{}, err
	}
	role := KeyRole(key)
	info, err := a.syncer.Discover(apiURL)
	if err != nil {
		return connectionPlan{}, err
	}
	entities, err := a.db.ListEntities()
	if err != nil {
		return connectionPlan{}, err
	}
	leaves, err := a.db.UsedNumbers()
	if err != nil {
		return connectionPlan{}, err
	}
	used := make([]string, 0, len(leaves))
	for n := range leaves {
		used = append(used, n)
	}
	sort.Strings(used)
	unsynced := len(used)+len(entities) > 0 && !a.syncer.EverAgreed()
	p := connectionPlan{
		Role:   role,
		Action: "connect",
		Server: obj{{"name", info.Name}, {"api_url", info.APIURL}},
		Local:  obj{{"entities", len(entities)}, {"leaves", used}, {"unsynced", unsynced}},
	}
	switch role {
	case "write":
		held := info.Register
		if held != nil && held.Held {
			agreed, ok := a.syncer.Agreement(info.APIURL)
			if !ok || held.Revision == nil || *held.Revision != agreed {
				p.Action = "replace"
			}
		}
	case "read":
		if len(used)+len(entities) > 0 {
			p.Action = "set-aside"
		}
	}
	return p, nil
}

// What adding this key would do. Nothing is sent to the server but the
// question of what it is: the key is not spent.
func (a *App) planConnection(rq *req) (*response, error) {
	sent := rq.loose()
	apiURL, _ := sent.get("api_url")
	userKey, _ := sent.get("user_key")
	p, err := a.plan(apiURL, userKey)
	if err != nil {
		var refused *connection.ConnectionError
		if errors.As(err, &refused) {
			return nil, refuse(http.StatusBadRequest, refused.Message)
		}
		return nil, err
	}
	return ok(p)
}

// Redeems the key with the server, which spends it, and keeps the token. What
// it does to this Mac's register is decided again here, and anything that
// sets the register aside needs the page to have confirmed that action.
func (a *App) putConnection(rq *req) (*response, error) {
	sent := rq.loose()
	apiURL, _ := sent.get("api_url")
	userKey, _ := sent.get("user_key")
	confirmed, _ := sent.get("confirm")
	key, _ := userKey.(string)
	if KeyRole(key) == "" {
		return a.withServer(func() error { return a.syncer.ConnectAs(apiURL, userKey, "", true) })
	}
	p, err := a.plan(apiURL, userKey)
	if err != nil {
		var refused *connection.ConnectionError
		if errors.As(err, &refused) {
			return nil, refuse(http.StatusBadRequest, refused.Message)
		}
		return nil, err
	}
	if p.Action != "connect" && confirmed != p.Action {
		return nil, refuse(http.StatusConflict, "Connecting with this key sets this Mac’s register aside. Confirm that first.")
	}
	restart := false
	reply, err := a.withServer(func() error {
		switch p.Action {
		case "replace":
			if err := a.syncer.ConnectAs(apiURL, userKey, "write", false); err != nil {
				return err
			}
			if err := a.syncer.FetchForRestore(); err != nil {
				return err
			}
			restart = true
		case "set-aside":
			if err := a.syncer.ConnectAs(apiURL, userKey, "read", false); err != nil {
				return err
			}
			if err := desksync.RequestSetAside(a.cfg.DBPath); err != nil {
				return err
			}
			restart = true
		default:
			return a.syncer.ConnectAs(apiURL, userKey, p.Role, true)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if restart && a.opts.OnRestoreReady != nil {
		a.opts.OnRestoreReady()
	}
	return reply, nil
}

// Puts back a register this Mac set aside when it was switched to a read key.
// Offered only while the register in use holds no leaves, so nothing written
// since is pushed aside unseen; even then the one it replaces is kept.
func (a *App) bringBack(rq *req) (*response, error) {
	sent := rq.loose()
	if confirm, _ := sent.get("confirm"); confirm != true {
		return nil, refuse(http.StatusBadRequest, "bringing a register back needs an explicit confirmation")
	}
	if a.syncer.ReadOnly() {
		return nil, refuse(http.StatusBadRequest, "This app is connected with a read key. Reset the key first.")
	}
	if leaves, err := a.db.UsedNumbers(); err != nil {
		return nil, err
	} else if len(leaves) > 0 {
		return nil, refuse(http.StatusBadRequest, "This Mac’s register already has leaves in it, so the one set aside cannot be brought back.")
	}
	name, _ := sent.get("name")
	text, _ := name.(string)
	if err := desksync.RequestBringBack(a.cfg.DBPath, text); err != nil {
		return nil, refuse(http.StatusBadRequest, err.Error())
	}
	if a.opts.OnRestoreReady != nil {
		a.opts.OnRestoreReady()
	}
	return ok(a.connectionNow())
}

// Is there a Banyan server at this address? Asked before a key is typed.
func (a *App) checkConnection(rq *req) (*response, error) {
	apiURL, _ := rq.loose().get("api_url")
	info, err := a.syncer.Discover(apiURL)
	if err != nil {
		var refused *connection.ConnectionError
		if errors.As(err, &refused) {
			return nil, refuse(http.StatusBadRequest, refused.Message)
		}
		return nil, err
	}
	return ok(obj{{"ok", true}, {"name", info.Name}, {"api_url", info.APIURL}})
}

// Forgets the key. The server's address is kept for next time unless the page
// asks for that to be forgotten too.
func (a *App) skipConnection(rq *req) (*response, error) {
	forget, _ := rq.loose().get("forget_server")
	if err := a.syncer.Disconnect(forget == true); err != nil {
		return nil, err
	}
	return ok(a.connectionNow())
}

// Send the register now and wait for the answer. The app calls this before it quits.
func (a *App) syncFlush(rq *req) (*response, error) { return a.withServer(a.syncer.Flush) }

// The treasurer chose the server's register over this desk's. It is fetched
// now and put in place when the app restarts; the register it replaces is kept.
func (a *App) syncRestore(rq *req) (*response, error) {
	if confirm, _ := rq.loose().get("confirm"); confirm != true {
		return nil, refuse(http.StatusBadRequest, "replacing this desk’s register needs an explicit confirmation")
	}
	reply, err := a.withServer(a.syncer.FetchForRestore)
	if err != nil {
		return nil, err
	}
	if a.opts.OnRestoreReady != nil {
		a.opts.OnRestoreReady()
	}
	return reply, nil
}

// readOnly answers the API of a reports-only app: its report pages come from
// the Banyan server, and everything else is closed.
func (a *App) readOnly(r *http.Request, path string) *response {
	refused := func(err error) *response {
		var c *connection.ConnectionError
		if errors.As(err, &c) {
			return mustJSON(http.StatusBadGateway, obj{{"detail", c.Message}})
		}
		return internalError(err)
	}
	if reading(r.Method) && path == "/api/reader" {
		reply, err := a.syncer.Read("/api/v1/register/status")
		if err != nil {
			return refused(err)
		}
		defer reply.Body.Close()
		var body struct {
			Latest json.RawMessage `json:"latest"`
			Detail *string         `json:"detail"`
		}
		data, _ := io.ReadAll(reply.Body)
		_ = json.Unmarshal(data, &body)
		if reply.StatusCode < 200 || reply.StatusCode > 299 {
			why := "The server answered " + strconv.Itoa(reply.StatusCode) + "."
			if body.Detail != nil {
				why = *body.Detail
			}
			return mustJSON(http.StatusOK, obj{{"app", true}, {"latest", nil}, {"error", why}})
		}
		var latest any
		if len(body.Latest) > 0 {
			latest = body.Latest
		}
		return mustJSON(http.StatusOK, obj{{"app", true}, {"role", "read"}, {"latest", latest}})
	}
	if reading(r.Method) && (path == "/api/reports" || strings.HasPrefix(path, "/api/reports/")) {
		_, _, rawQuery := target(r)
		if rawQuery == "?" {
			rawQuery = ""
		}
		reply, err := a.syncer.Read("/api/v1" + path[len("/api"):] + rawQuery)
		if err != nil {
			return refused(err)
		}
		defer reply.Body.Close()
		data, err := io.ReadAll(reply.Body)
		if err != nil {
			return refused(notConnected("The Banyan server could not be reached. Check the internet connection."))
		}
		res := &response{status: reply.StatusCode, header: [][2]string{{"Cache-Control", "no-store"}}, body: data}
		for _, name := range []string{"Content-Type", "Content-Disposition"} {
			if value := reply.Header.Get(name); value != "" {
				res.header = append(res.header, [2]string{name, value})
			}
		}
		return res
	}
	// A head or a note: the one thing a reader may change, sent to the server as this reader.
	if r.Method == http.MethodPost && path == "/api/annotations" {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			return refused(notConnected("That edit could not be read."))
		}
		reply, err := a.syncer.Send(http.MethodPost, "/api/v1/annotations", body)
		if err != nil {
			return refused(err)
		}
		defer reply.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(reply.Body, 1<<20))
		return &response{status: reply.StatusCode,
			header: [][2]string{{"Content-Type", "application/json"}, {"Cache-Control", "no-store"}}, body: data}
	}
	return mustJSON(http.StatusForbidden, obj{{"detail", "This app is connected with a read key. It shows reports only."}})
}
