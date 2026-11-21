package update

import "crypto/ed25519"

// publicKey is the half of the release signing key the app carries. The
// private half is the UPDATE_SIGNING_KEY secret of the release workflow.
// Replacing it means every app already installed stops accepting updates,
// so a new key reaches them only by a manual download.
const publicKey = "sISzkmgDVwNo0WEbulHpO1tzrtGTBpZo4ZkdhoM+sNU="

// PublicKey is the key releases are checked against.
func PublicKey() ed25519.PublicKey { return PublicKeyFrom(publicKey) }
