//go:build !mas

package main

// appStore is false for the direct download: signed with a Developer ID,
// updated by its own signed updater, and not sandboxed.
const appStore = false
