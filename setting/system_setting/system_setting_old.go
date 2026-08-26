package system_setting

var ServerAddress = "http://localhost:3000"

// FrontendAddress is the base for links a USER opens from an email. Empty falls
// back to ServerAddress, which keeps a standalone deployment working. It is
// separate because ServerAddress also derives the passkey RPID and builds
// Midjourney image URLs, so pointing those at a separate frontend would break
// them.
var FrontendAddress = ""

// UserLinkBase returns the base for user-facing links.
func UserLinkBase() string {
	if FrontendAddress != "" {
		return FrontendAddress
	}
	return ServerAddress
}
var WorkerUrl = ""
var WorkerValidKey = ""
var WorkerAllowHttpImageRequestEnabled = false

func EnableWorker() bool {
	return WorkerUrl != ""
}
