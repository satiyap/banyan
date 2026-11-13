package desk

import (
	"net/http"

	"github.com/satiyap/banyan/internal/desksync"
)

// serverSync is the desk's Syncer over internal/desksync.
type serverSync struct {
	sync *desksync.Sync
}

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
	return ServerInfo{Name: found.Name, APIURL: found.ApiUrl}, err
}

func (s serverSync) Connect(apiURL, userKey any) error {
	_, err := s.sync.Connect(apiURL, userKey)
	return err
}

func (s serverSync) Disconnect(forgetServer bool) error {
	_, err := s.sync.Disconnect(forgetServer)
	return err
}

func (s serverSync) Flush() error {
	s.sync.Flush()
	return nil
}

func (s serverSync) Overwrite() error {
	s.sync.Overwrite()
	return nil
}

func (s serverSync) FetchForRestore() error {
	_, err := s.sync.FetchForRestore()
	return err
}

func (s serverSync) Read(path string) (*http.Response, error) { return s.sync.Read(path) }
