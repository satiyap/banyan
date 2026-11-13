package desk

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/satiyap/banyan/internal/connection"
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
}

// Syncer keeps the Banyan server's copy of the register level with this
// desk's. A refusal meant for the treasurer is a *connection.ConnectionError.
type Syncer interface {
	// ReadOnly is true when this app was connected with a read key: it shows reports and nothing else.
	ReadOnly() bool
	Status() SyncStatus
	// Discover asks an address whether it is a Banyan server this desk can work with.
	Discover(apiURL any) (ServerInfo, error)
	// Connect redeems a key for this desk and remembers the token it buys.
	Connect(apiURL, userKey any) error
	// Disconnect forgets the key, and the server's address too when asked.
	Disconnect(forgetServer bool) error
	// Schedule notes that the register may have changed, and sends it shortly.
	Schedule()
	// Flush sends the register now if the server does not have it, and waits for the answer.
	Flush() error
	// Overwrite replaces the server's register with this desk's.
	Overwrite() error
	// FetchForRestore fetches the server's register and leaves it beside this
	// desk's, to be put in place when the app restarts.
	FetchForRestore() error
	// Read fetches something a reader may see from the Banyan server, as that
	// reader. The caller closes the body.
	Read(path string) (*http.Response, error)
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
func (offline) Overwrite() error   { return nil }

const noServer = "Connecting to a Banyan server is not available in this build."

func (offline) Discover(any) (ServerInfo, error) { return ServerInfo{}, notConnected(noServer) }
func (offline) Connect(any, any) error           { return notConnected(noServer) }

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

// ------------------------------------------------------- server connection
// The desktop app asks once, on first launch, for the API URL and user key of
// the Banyan server, and can be told to skip.

type connectionNow struct {
	connection.Description
	Sync        SyncStatus `json:"sync"`
	DeskVersion string     `json:"desk_version"`
}

func (a *App) connectionNow() connectionNow {
	return connectionNow{
		Description: connection.DescribeConnection(connection.ReadConnection(a.cfg.ConnectionPath)),
		Sync:        a.syncer.Status(),
		DeskVersion: a.version,
	}
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

// Redeems the key with the server, which spends it, and keeps the token.
func (a *App) putConnection(rq *req) (*response, error) {
	sent := rq.loose()
	apiURL, _ := sent.get("api_url")
	userKey, _ := sent.get("user_key")
	return a.withServer(func() error { return a.syncer.Connect(apiURL, userKey) })
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

// The treasurer chose this desk's register over the server's.
func (a *App) syncOverwrite(rq *req) (*response, error) {
	if confirm, _ := rq.loose().get("confirm"); confirm != true {
		return nil, refuse(http.StatusBadRequest, "replacing the server’s register needs an explicit confirmation")
	}
	return a.withServer(a.syncer.Overwrite)
}

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
	return mustJSON(http.StatusForbidden, obj{{"detail", "This app is connected with a read key. It shows reports only."}})
}
