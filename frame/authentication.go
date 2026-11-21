package frame

import "time"

// Authentication is a verifier's account of how and when a party authenticated.
// It neither widens a grant nor establishes that the party is a human. A method
// such as session says how this call was admitted, not whether MFA occurred.
type Authentication struct {
	Method string
	// Date is the original authentication time, when known. Credential issuance
	// and renewal do not refresh it. Zero means the verifier did not know.
	Date time.Time
}

// Fresh checks the recorded time against the application's clock and window.
// Unknown and future times, or a non-positive window, fail closed. Applications
// must also decide which parties, methods and proof levels their action needs.
func (a Authentication) Fresh(now time.Time, window time.Duration) bool {
	return window > 0 && !a.Date.IsZero() && !a.Date.After(now) && now.Sub(a.Date) <= window
}
