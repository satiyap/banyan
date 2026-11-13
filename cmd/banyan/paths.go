package main

import (
	"io/fs"
	"os"
	"path/filepath"

	banyan "github.com/satiyap/banyan"
)

// paths is where the desk keeps its files. They live in the app's data
// folder, so they survive updates.
type paths struct {
	data        string
	register    string // the register: financial records
	calibration string // measurements of this printer, the treasurer's to edit
	pdfDir      string // rendered cheques and reports, disposable
	dataStatic  string // where a prepared leaf photo may live
	connection  string // the server address and token, or the fact that it was skipped
}

// preparePaths makes the data folder and, on first run, a copy of the shipped
// calibration.
//
// A release build uses the folder version 1 used, so it opens the same
// register. A development build keeps to a folder of its own, so unreleased
// code never opens the real one. BANYAN_DATA_DIR overrides both.
func preparePaths() (paths, error) {
	data := os.Getenv("BANYAN_DATA_DIR")
	if data == "" {
		base, err := os.UserConfigDir() // ~/Library/Application Support
		if err != nil {
			return paths{}, err
		}
		name := "Banyan"
		if isDev() {
			name = "Banyan Dev"
		}
		data = filepath.Join(base, name)
	}
	p := paths{
		data:        data,
		register:    filepath.Join(data, "banyan.db"),
		calibration: filepath.Join(data, "cheque-calibration.json"),
		pdfDir:      filepath.Join(data, "cheques"),
		dataStatic:  filepath.Join(data, "static"),
		connection:  filepath.Join(data, "connection.json"),
	}
	if err := os.MkdirAll(p.pdfDir, 0o755); err != nil {
		return paths{}, err
	}
	if _, err := os.Stat(p.calibration); os.IsNotExist(err) {
		shipped, err := fs.ReadFile(banyan.Assets, "cheque-calibration.json")
		if err != nil {
			return paths{}, err
		}
		if err := os.WriteFile(p.calibration, shipped, 0o644); err != nil {
			return paths{}, err
		}
	}
	return p, nil
}
