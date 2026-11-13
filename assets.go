// Package banyan carries the files the app serves and prints with: the desk's
// pages, the cheque print template and the measured calibration. They are
// compiled into the binary, so the app is one file.
package banyan

import "embed"

//go:embed static templates cheque-calibration.json
var Assets embed.FS
