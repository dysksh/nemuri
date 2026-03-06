package state

import "fmt"

// JobState represents the current state of a job.
type JobState string

const (
	StateInit             JobState = "INIT"
	StateRunning          JobState = "RUNNING"
	StateWaitingUserInput JobState = "WAITING_USER_INPUT"
	StateReadyForPR       JobState = "READY_FOR_PR"
	StateWaitingApproval  JobState = "WAITING_APPROVAL"
	StateDone             JobState = "DONE"
	StateFailed           JobState = "FAILED"
)

// allowedTransitions defines the valid state transitions.
var allowedTransitions = map[JobState][]JobState{
	StateInit:             {StateRunning},
	StateRunning:          {StateWaitingUserInput, StateReadyForPR, StateDone, StateFailed},
	StateWaitingUserInput: {StateRunning},
	StateReadyForPR:       {StateWaitingApproval},
	StateWaitingApproval:  {StateDone},
}

// ValidateTransition checks if transitioning from `from` to `to` is allowed.
func ValidateTransition(from, to JobState) error {
	allowed, ok := allowedTransitions[from]
	if !ok {
		return fmt.Errorf("no transitions allowed from state %s", from)
	}
	for _, s := range allowed {
		if s == to {
			return nil
		}
	}
	return fmt.Errorf("transition from %s to %s is not allowed", from, to)
}
