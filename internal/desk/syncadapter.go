package desk

import (
	"net/http"

	"github.com/satiyap/banyan/internal/desksync"
)

// serverSync is the desk's Syncer over internal/desksync.
type serverSync struct {
	sync *desksync.Sync
}

// ServerSync is a Syncer over an existing link to the Banyan server.
func ServerSync(s *desksync.Sync) Syncer { return serverSync{s} }

// defaultSyncer is the link to the Banyan server named in the connection file.
func defaultSyncer(cfg Config) Syncer {
	return serverSync{desksync.NewSync(desksync.SyncPaths{DbPath: cfg.DBPath, ConnectionPath: cfg.ConnectionPath}, desksync.Options{})}
}

func (s serverSync) ReadOnly() bool { return s.sync.ReadOnly() }
func (s serverSync) Schedule()      { s.sync.Schedule() }
func (s serverSync) Close()         { s.sync.Close() }

func (s serverSync) Status() SyncStatus {
	status := s.sync.Status()
	out := SyncStatus{
		Connected: status.Connected,
		ReadOnly:  status.ReadOnly,
		APIURL:    status.ApiUrl,
		SyncedAt:  status.SyncedAt,
		Revision:  status.Revision,
		Pending:   status.Pending,
		Error:     status.Error,
		Retired:   status.Retired,
	}
	if c := status.Choice; c != nil {
		out.Choice = &Revision{Revision: c.Revision, SHA256: c.Sha256, Bytes: c.Bytes, SyncedAt: c.SyncedAt}
	}
	return out
}

func (s serverSync) Discover(apiURL any) (ServerInfo, error) {
	found, err := s.sync.Discover(apiURL)
	info := ServerInfo{Name: found.Name, APIURL: found.ApiUrl}
	if found.Register != nil {
		info.Register = &HeldRegister{Held: found.Register.Held, Revision: found.Register.Revision}
	}
	return info, err
}

func (s serverSync) ConnectAs(apiURL, userKey any, want string, flush bool) error {
	_, err := s.sync.ConnectAs(apiURL, userKey, want, flush)
	return err
}

func (s serverSync) Agreement(apiURL string) (int64, bool) {
	revision, _, ok := s.sync.Agreement(apiURL)
	return revision, ok
}

func (s serverSync) EverAgreed() bool { return s.sync.EverAgreed() }

func (s serverSync) Disconnect(forgetServer bool) error {
	_, err := s.sync.Disconnect(forgetServer)
	return err
}

func (s serverSync) Flush() error {
	s.sync.Flush()
	return nil
}

func (s serverSync) FetchForRestore() error {
	_, err := s.sync.FetchForRestore()
	return err
}

func (s serverSync) Read(path string) (*http.Response, error) { return s.sync.Read(path) }

func (s serverSync) Send(method, path string, body []byte) (*http.Response, error) {
	return s.sync.Send(method, path, body)
}
