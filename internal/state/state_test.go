package state_test

import (
	"testing"

	"github.com/nemuri/nemuri/internal/state"
)

func TestValidateTransition(t *testing.T) {
	tests := []struct {
		name    string
		from    state.JobState
		to      state.JobState
		wantErr bool
	}{
		// INIT transitions
		{"INIT→RUNNING", state.StateInit, state.StateRunning, false},
		{"INIT→DONE rejected", state.StateInit, state.StateDone, true},
		{"INIT→FAILED rejected", state.StateInit, state.StateFailed, true},

		// RUNNING transitions
		{"RUNNING→WAITING_USER_INPUT", state.StateRunning, state.StateWaitingUserInput, false},
		{"RUNNING→DONE", state.StateRunning, state.StateDone, false},
		{"RUNNING→FAILED", state.StateRunning, state.StateFailed, false},
		{"RUNNING→INIT rejected", state.StateRunning, state.StateInit, true},

		// WAITING_USER_INPUT transitions
		{"WAITING_USER_INPUT→RUNNING", state.StateWaitingUserInput, state.StateRunning, false},
		{"WAITING_USER_INPUT→DONE rejected", state.StateWaitingUserInput, state.StateDone, true},

		// WAITING_APPROVAL transitions
		{"WAITING_APPROVAL→DONE", state.StateWaitingApproval, state.StateDone, false},
		{"WAITING_APPROVAL→RUNNING rejected", state.StateWaitingApproval, state.StateRunning, true},

		// Terminal states
		{"DONE→any rejected", state.StateDone, state.StateRunning, true},
		{"FAILED→any rejected", state.StateFailed, state.StateRunning, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := state.ValidateTransition(tt.from, tt.to)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateTransition(%s, %s) error = %v, wantErr %v", tt.from, tt.to, err, tt.wantErr)
			}
		})
	}
}
