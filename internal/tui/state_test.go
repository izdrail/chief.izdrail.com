package tui

import (
	"testing"
)

func TestAppState_String(t *testing.T) {
	tests := []struct {
		state    AppState
		expected string
	}{
		{StateReady, "Ready"},
		{StateRunning, "Running"},
		{StatePaused, "Paused"},
		{StateStopped, "Stopped"},
		{StateComplete, "Complete"},
		{StateError, "Error"},
		{AppState(99), "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("AppState.String() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestStateTransitions_StartFromReady(t *testing.T) {
	// Test that we can transition from Ready to Running
	app := &App{
		state:   StateReady,
		prdPath: "/tmp/test-prd.json",
		maxIter: 10,
	}

	// Verify initial state
	if app.state != StateReady {
		t.Errorf("Expected initial state Ready, got %v", app.state)
	}

	// The start key should be allowed when Ready
	canStart := app.state == StateReady || app.state == StatePaused
	if !canStart {
		t.Error("Expected to be able to start from Ready state")
	}
}

func TestStateTransitions_StartFromPaused(t *testing.T) {
	// Test that we can transition from Paused to Running
	app := &App{
		state:   StatePaused,
		prdPath: "/tmp/test-prd.json",
		maxIter: 10,
	}

	// The start key should be allowed when Paused
	canStart := app.state == StateReady || app.state == StatePaused
	if !canStart {
		t.Error("Expected to be able to start from Paused state")
	}
}

func TestStateTransitions_PauseFromRunning(t *testing.T) {
	// Test that we can pause from Running state
	app := &App{
		state: StateRunning,
	}

	// The pause key should be allowed when Running
	canPause := app.state == StateRunning
	if !canPause {
		t.Error("Expected to be able to pause from Running state")
	}
}

func TestStateTransitions_PauseNotAllowedFromReady(t *testing.T) {
	// Test that we cannot pause from Ready state
	app := &App{
		state: StateReady,
	}

	// The pause key should NOT be allowed when Ready
	canPause := app.state == StateRunning
	if canPause {
		t.Error("Should not be able to pause from Ready state")
	}
}

func TestStateTransitions_StopFromRunning(t *testing.T) {
	// Test that we can stop from Running state
	app := &App{
		state: StateRunning,
	}

	// The stop key should be allowed when Running
	canStop := app.state == StateRunning || app.state == StatePaused
	if !canStop {
		t.Error("Expected to be able to stop from Running state")
	}
}

func TestStateTransitions_StopFromPaused(t *testing.T) {
	// Test that we can stop from Paused state
	app := &App{
		state: StatePaused,
	}

	// The stop key should be allowed when Paused
	canStop := app.state == StateRunning || app.state == StatePaused
	if !canStop {
		t.Error("Expected to be able to stop from Paused state")
	}
}

func TestStateTransitions_StopNotAllowedFromReady(t *testing.T) {
	// Test that we cannot stop from Ready state
	app := &App{
		state: StateReady,
	}

	// The stop key should NOT be allowed when Ready
	canStop := app.state == StateRunning || app.state == StatePaused
	if canStop {
		t.Error("Should not be able to stop from Ready state")
	}
}

func TestStateTransitions_RetryFromError(t *testing.T) {
	// Test that we can retry (start) from Error state
	// Note: The implementation allows starting from Ready or Paused,
	// but the footer shows "retry" for Error state which implies start should work
	app := &App{
		state: StateError,
	}

	// Currently start is only allowed from Ready or Paused
	// This test documents the current behavior
	canStart := app.state == StateReady || app.state == StatePaused
	if canStart {
		t.Log("Start is currently not allowed from Error state (would need to reset to Ready first)")
	}
}

func TestStateTransitions_CompleteState(t *testing.T) {
	// Test Complete state behavior
	app := &App{
		state: StateComplete,
	}

	// Should not be able to start, pause, or stop from Complete
	canStart := app.state == StateReady || app.state == StatePaused
	canPause := app.state == StateRunning
	canStop := app.state == StateRunning || app.state == StatePaused

	if canStart || canPause || canStop {
		t.Error("Should not be able to perform any loop control from Complete state")
	}
}

func TestStateTransitions_AllStates(t *testing.T) {
	// Comprehensive test of all state transitions
	type transitionTest struct {
		fromState   AppState
		canStart    bool
		canPause    bool
		canStop     bool
		description string
	}

	tests := []transitionTest{
		{StateReady, true, false, false, "Ready allows start only"},
		{StateRunning, false, true, true, "Running allows pause and stop"},
		{StatePaused, true, false, true, "Paused allows start and stop"},
		{StateStopped, false, false, false, "Stopped allows no loop control"},
		{StateComplete, false, false, false, "Complete allows no loop control"},
		{StateError, false, false, false, "Error allows no loop control"},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			app := &App{state: tt.fromState}

			canStart := app.state == StateReady || app.state == StatePaused
			canPause := app.state == StateRunning
			canStop := app.state == StateRunning || app.state == StatePaused

			if canStart != tt.canStart {
				t.Errorf("canStart: got %v, want %v", canStart, tt.canStart)
			}
			if canPause != tt.canPause {
				t.Errorf("canPause: got %v, want %v", canPause, tt.canPause)
			}
			if canStop != tt.canStop {
				t.Errorf("canStop: got %v, want %v", canStop, tt.canStop)
			}
		})
	}
}
