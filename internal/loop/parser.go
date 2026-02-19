package loop

// EventType represents the type of event emitted by the agent loop.
type EventType int

const (
	// EventUnknown represents an unrecognized event type.
	EventUnknown EventType = iota
	// EventIterationStart is emitted at the start of an Ollama agent iteration.
	EventIterationStart
	// EventAssistantText is emitted when the model outputs text.
	EventAssistantText
	// EventToolStart is emitted when the model invokes a tool.
	EventToolStart
	// EventToolResult is emitted when a tool returns a result.
	EventToolResult
	// EventStoryStarted is emitted when the model indicates a story is being worked on.
	EventStoryStarted
	// EventStoryCompleted is emitted when the model completes a story.
	EventStoryCompleted
	// EventComplete is emitted when <chief-complete/> is detected.
	EventComplete
	// EventMaxIterationsReached is emitted when max iterations are reached.
	EventMaxIterationsReached
	// EventError is emitted when an error occurs.
	EventError
	// EventRetrying is emitted when retrying after an error.
	EventRetrying
)

// String returns the string representation of an EventType.
func (e EventType) String() string {
	switch e {
	case EventIterationStart:
		return "IterationStart"
	case EventAssistantText:
		return "AssistantText"
	case EventToolStart:
		return "ToolStart"
	case EventToolResult:
		return "ToolResult"
	case EventStoryStarted:
		return "StoryStarted"
	case EventStoryCompleted:
		return "StoryCompleted"
	case EventComplete:
		return "Complete"
	case EventMaxIterationsReached:
		return "MaxIterationsReached"
	case EventError:
		return "Error"
	case EventRetrying:
		return "Retrying"
	default:
		return "Unknown"
	}
}

// Event represents an event emitted by the agent loop.
type Event struct {
	Type       EventType
	Iteration  int
	Text       string
	Tool       string
	ToolInput  map[string]interface{}
	StoryID    string
	Err        error
	RetryCount int // Current retry attempt (1-based)
	RetryMax   int // Maximum retries allowed
}
