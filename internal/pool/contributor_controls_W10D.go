// File: pools-core/internal/pool/contributor_controls_W10D.go (go)
// W10D row 1 (D-W10-11, CR-51): the admin controlled contributor controls of a
// pool. off is the default and keeps the page of every earlier round: no
// buttons, connect and compute at once.
package pool

import "fmt"

// ContributorControls selects the control section a contributor tab shows.
type ContributorControls string

// The three documented modes. A contributor tab reads the mode at registration
// and on every heartbeat, so a Director change applies on the next heartbeat.
const (
	ContributorControlsOff            ContributorControls = "off"
	ContributorControlsPauseResume    ContributorControls = "pauseResume"
	ContributorControlsStartStopOptIn ContributorControls = "startStopOptIn"
)

// ContributorControlsDefault is the documented default: today's page, no buttons.
const ContributorControlsDefault = ContributorControlsOff

// ContributorControlsModes lists every selectable value in panel order.
func ContributorControlsModes() []string {
	return []string{
		string(ContributorControlsOff),
		string(ContributorControlsPauseResume),
		string(ContributorControlsStartStopOptIn),
	}
}

// ParseContributorControls validates one Director supplied value. Unknown and
// empty values are refused; they never weaken the default.
func ParseContributorControls(value string) (ContributorControls, error) {
	switch mode := ContributorControls(value); mode {
	case ContributorControlsOff, ContributorControlsPauseResume, ContributorControlsStartStopOptIn:
		return mode, nil
	}
	return "", fmt.Errorf("%w: unknown contributorControls %q", ErrInvalid, value)
}
