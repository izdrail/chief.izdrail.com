# Chief TUI - Feature Specification

## Overview

Chief is an autonomous agent loop that orchestrates Ollama Code to work through PRD user stories. This spec describes a TUI application that wraps the agent loop with monitoring, controls, and a delightful developer experience.


## Goals

1. **Delightful DX** - Make monitoring and controlling the agent loop a pleasure
2. **Easy Distribution** - Single binary, no dependencies, cross-platform
3. **Simple Core** - The actual loop should be ~80 lines, easy to understand and debug
4. **Self-Contained** - Embed the agent prompt, PRD skills, and completion sound

## Non-Goals

- Branch management (removed - let users handle git themselves)
- Headless/CI mode (not needed for v1)
- Settings persistence (CLI flags are sufficient)

## Technology Choice: Go + Bubble Tea

**Why Go?**
- Single binary distribution (no runtime dependencies)
- Cross-compilation via goreleaser (darwin/linux/windows, amd64/arm64)
- Built-in JSON parsing, no external deps needed
- Excellent TUI library ecosystem

**Why Bubble Tea?**
- Modern, composable TUI framework
- Great keyboard handling and focus management
- Built-in support for async operations
- Active community and maintenance

**Alternatives Considered:**
| Option | Pros | Cons |
|--------|------|------|
| Bash + dialog | Simple | Limited, ugly, no Windows |
| Rust + ratatui | Fast, single binary | Steeper learning curve |
| Python + textual | Quick to build | Requires Python runtime |
| Node + ink | React-like | Requires Node runtime |

## Architecture

```
chief/
├── cmd/chief/
│   └── main.go                  # CLI entry, flag parsing
├── internal/
│   ├── loop/
│   │   ├── loop.go              # Core loop (~80 lines)
│   │   └── parser.go            # Parse stream-json → events
│   ├── prd/
│   │   ├── types.go             # PRD structs
│   │   ├── loader.go            # Load, watch, list PRDs from .chief/prds/
│   │   └── generator.go         # `chief new` (launches Claude)
│   ├── progress/
│   │   └── progress.go          # Append to progress.md
│   ├── tui/
│   │   ├── app.go               # Main Bubble Tea model
│   │   ├── dashboard.go         # Dashboard view (tasks + details)
│   │   ├── log.go               # Pretty log viewer
│   │   ├── picker.go            # PRD picker modal
│   │   └── styles.go            # Lip Gloss styles
│   └── notify/
│       └── sound.go             # Embed + play completion sound
├── embed/
│   ├── prompt.txt               # Agent prompt
│   ├── prd_skill.txt            # PRD generator prompt
│   ├── convert_skill.txt        # PRD→JSON converter prompt
│   └── complete.wav             # ~30KB completion chime
└── go.mod
```

## Core Loop Design

The loop must be **dead simple** - anyone reading the code should immediately understand it.

### The Loop in Plain English

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           CHIEF LOOP MECHANICS                              │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  1. READ STATE                                                              │
│     └── Load prd.json to check for incomplete stories                       │
│                                                                             │
│  2. BUILD PROMPT                                                            │
│     └── Combine: embedded agent prompt + PRD path + current story context   │
│                                                                             │
│  3. INVOKE CLAUDE                                                           │
│     └── claude --dangerously-skip-permissions -p <prompt> \                 │
│               --output-format stream-json --verbose                         │
│                                                                             │
│  4. STREAM OUTPUT                                                           │
│     ├── Parse each JSON line from stdout                                    │
│     ├── Extract: assistant text, tool calls, tool results                   │
│     ├── Send events to TUI for display                                      │
│     └── Append raw output to claude.log                                     │
│                                                                             │
│  5. WAIT FOR EXIT                                                           │
│     ├── Claude exits when it completes a story (or errors)                  │
│     └── Check exit code: 0 = success, non-zero = error                      │
│                                                                             │
│  6. CHECK COMPLETION                                                        │
│     ├── Re-read prd.json (Claude updated it)                                │
│     ├── If all stories pass: emit <chief-complete/>, play sound, stop       │
│     ├── If iteration < max: goto step 1                                     │
│     └── If iteration >= max: stop, notify user                              │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Code (~80 lines total)

```go
// internal/loop/loop.go

type Loop struct {
    prdPath    string
    prompt     string
    maxIter    int
    iteration  int
    events     chan Event  // Send to TUI
    claudeCmd  *exec.Cmd
}

// Run executes the full loop until complete or max iterations
func (l *Loop) Run(ctx context.Context) error {
    for l.iteration < l.maxIter {
        l.iteration++
        l.events <- Event{Type: IterationStart, Iteration: l.iteration}

        if err := l.runIteration(ctx); err != nil {
            if ctx.Err() != nil {
                return ctx.Err()  // User cancelled
            }
            l.events <- Event{Type: Error, Err: err}
            continue  // Try next iteration
        }

        // Check if all stories complete
        prd, _ := LoadPRD(l.prdPath)
        if prd.AllComplete() {
            l.events <- Event{Type: Complete}
            return nil
        }
    }
    l.events <- Event{Type: MaxIterationsReached}
    return nil
}

// runIteration executes a single Claude invocation
func (l *Loop) runIteration(ctx context.Context) error {
    l.claudeCmd = exec.CommandContext(ctx, "claude",
        "--dangerously-skip-permissions",
        "-p", l.prompt,
        "--output-format", "stream-json",
        "--verbose",
    )

    stdout, _ := l.claudeCmd.StdoutPipe()
    l.claudeCmd.Start()

    // Stream and parse output
    scanner := bufio.NewScanner(stdout)
    for scanner.Scan() {
        line := scanner.Text()
        l.logToFile(line)
        if event := l.parseLine(line); event != nil {
            l.events <- *event
        }
    }

    return l.claudeCmd.Wait()
}

// Stop kills the Claude process (for 'x' key)
func (l *Loop) Stop() {
    if l.claudeCmd != nil && l.claudeCmd.Process != nil {
        l.claudeCmd.Process.Kill()
    }
}
```

### Stream-JSON Format

Claude's `--output-format stream-json` emits one JSON object per line:

```jsonl
{"type":"assistant","message":{"content":[{"type":"text","text":"Let me read the PRD..."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":".chief/prds/main/prd.json"}}]}}
{"type":"tool_result","content":"{\n  \"project\": \"..."}
{"type":"assistant","message":{"content":[{"type":"text","text":"I'll work on US-001..."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"src/app.ts"}}]}}
{"type":"tool_result","content":"File edited successfully"}
{"type":"result","result":"Story US-001 complete. Updated prd.json."}
```

### Parser Events

```go
// internal/loop/parser.go

type EventType int

const (
    IterationStart EventType = iota
    AssistantText           // Claude is "thinking" - show in log
    ToolStart               // Tool invocation started
    ToolResult              // Tool completed
    StoryStarted            // Claude set inProgress: true
    StoryCompleted          // Claude set passes: true
    Complete                // All stories done (<chief-complete/>)
    MaxIterationsReached
    Error
)

type Event struct {
    Type      EventType
    Iteration int
    Text      string      // For AssistantText
    Tool      string      // For ToolStart/ToolResult (Read, Edit, Bash, etc.)
    ToolInput string      // Tool arguments (file path, command, etc.)
    StoryID   string      // For StoryStarted/StoryCompleted
    Err       error       // For Error
}

func (l *Loop) parseLine(line string) *Event {
    var msg StreamMessage
    json.Unmarshal([]byte(line), &msg)

    switch msg.Type {
    case "assistant":
        // Check content blocks for text vs tool_use
        for _, block := range msg.Message.Content {
            if block.Type == "text" {
                // Check for <chief-complete/>
                if strings.Contains(block.Text, "<chief-complete/>") {
                    return &Event{Type: Complete}
                }
                return &Event{Type: AssistantText, Text: block.Text}
            }
            if block.Type == "tool_use" {
                return &Event{Type: ToolStart, Tool: block.Name, ToolInput: block.Input}
            }
        }
    case "tool_result":
        return &Event{Type: ToolResult}
    }
    return nil
}
```

### How Claude Knows What To Do

The prompt (embedded in Chief) tells Claude:

1. **Where to find the PRD**: `.chief/prds/<name>/prd.json`
2. **How to pick the next story**: First `inProgress: true`, then lowest priority with `passes: false`
3. **How to mark progress**: Update `inProgress` and `passes` fields in prd.json
4. **How to signal completion**: Output `<chief-complete/>` when all stories pass
5. **What to log**: Append to progress.md after each story

Claude is autonomous within an iteration — Chief just watches and displays.

### Key Principle

**No magic. Just `claude` with flags.**

The entire system is:
1. A prompt that tells Claude how to work through stories
2. A JSON file that tracks state
3. A TUI that displays progress
4. A loop that keeps invoking Claude until done

## File Structure

When Chief runs in a project:

```
your-project/
├── .chief/
│   └── prds/
│       ├── main/                 # Default PRD
│       │   ├── prd.md            # Human-readable PRD (from `chief new`)
│       │   ├── prd.json          # Machine-readable PRD (auto-generated from prd.md)
│       │   ├── progress.md       # Human-readable progress log
│       │   └── claude.log        # Raw Claude output
│       ├── auth/                 # Additional PRD
│       │   ├── prd.md
│       │   ├── prd.json
│       │   ├── progress.md
│       │   └── claude.log
│       └── api/                  # Another PRD
│           └── ...
├── src/
└── ...
```

Each PRD lives in its own directory with all related files. The directory name is the PRD identifier used in CLI commands.

## PRD Schema

```json
{
  "project": "Project Name",
  "description": "Feature description",
  "userStories": [
    {
      "id": "US-001",
      "title": "Story title",
      "description": "As a..., I need... so that...",
      "acceptanceCriteria": [
        "Criterion 1",
        "Criterion 2",
        "Typecheck passes"
      ],
      "priority": 1,
      "passes": false
    }
  ]
}
```

**Priority ordering:** Lower number = higher priority = do first. Stories should be ordered by dependency (schema → backend → frontend → polish).

**Status tracking via PRD (set by Claude at runtime):**
- `inProgress: true` - Claude sets this when starting a story
- `passes: true` - Claude sets this when story is complete
- `inProgress: false` - Claude sets this when story is complete (along with passes)
- The TUI watches prd.json for changes to update the display

**Note:** `inProgress` is not in the initial prd.json — Claude adds it at runtime.

## CLI Interface

```bash
# Main usage
chief                      # Run default PRD (.chief/prds/main/), start TUI
chief auth                 # Run specific PRD by name (.chief/prds/auth/)
chief ./path/to/prd.json   # Run PRD from explicit path

# PRD generation
chief new                 # Create new PRD in .chief/prds/main/
chief new auth            # Create new PRD in .chief/prds/auth/
chief new auth "login"    # Create with initial context for "login"
chief edit                 # Edit existing PRD (default: main)
chief edit auth            # Edit specific PRD

# Options
chief --max-iterations 40  # Iteration limit (default: 10)
chief --no-sound           # Disable completion sound
chief --verbose            # Show raw Claude output in log

# Note: One iteration = one Claude invocation = typically one story.
# If you have 15 stories, set --max-iterations to at least 15.
# The limit prevents runaway loops and excessive API usage.

# Quick commands (no TUI)
chief status               # Print current progress for default PRD
chief status auth          # Print progress for specific PRD
chief list                 # List all PRDs in .chief/prds/
```

## Auto-Conversion

**prd.md is the source of truth.** Users only edit prd.md — Chief handles conversion automatically.

### When Conversion Happens

1. **After `chief new`** — Automatically converts prd.md → prd.json
2. **After `chief edit`** — Automatically converts prd.md → prd.json
3. **Before `chief run`** — If prd.md is newer than prd.json, converts first

### Progress Protection

If prd.json has existing progress (any story with `passes: true` or `inProgress: true`), Chief warns before overwriting:

```
╭─ Warning ──────────────────────────────────────────────────────────────────────╮
│                                                                                │
│  prd.md has changed, but prd.json has progress:                                │
│                                                                                │
│    ✓  US-001  Set up Tailwind CSS with base config                             │
│    ✓  US-002  Configure design tokens                                          │
│    ▶  US-003  Create color theme system  (in progress)                         │
│                                                                                │
│  How would you like to proceed?                                                │
│                                                                                │
│    [M] Merge — Keep status for matching story IDs, add new stories             │
│    [O] Overwrite — Regenerate prd.json (lose all progress)                     │
│    [C] Cancel — Keep existing prd.json, don't convert                          │
│                                                                                │
╰────────────────────────────────────────────────────────────────────────────────╯
```

**Merge behavior:**
- Stories with matching IDs keep their `passes` and `inProgress` status
- New stories in prd.md are added with `passes: false`
- Stories removed from prd.md are dropped from prd.json
- Story content (title, description, acceptance criteria) updates from prd.md

**CLI flags for non-interactive use:**
```bash
chief --merge              # Auto-merge without prompting
chief --force              # Auto-overwrite without prompting
```

## TUI Design

### Design Principles

- **Modern & minimal** — Clean lines, generous spacing, clear hierarchy
- **Information-dense but not cluttered** — Show what matters, hide what doesn't
- **Keyboard-first** — All actions accessible via keyboard, shortcuts always visible
- **Status at a glance** — Current state obvious within 1 second of looking
- **Responsive** — Gracefully handles narrow terminals (min 80 cols) and wide terminals (120+ cols)

### Color Palette (Lip Gloss)

| Element | Color | Hex |
|---------|-------|-----|
| Primary accent | Cyan | `#00D7FF` |
| Success | Green | `#5AF78E` |
| Warning | Yellow | `#F3F99D` |
| Error | Red | `#FF5C57` |
| Muted text | Gray | `#6C7086` |
| Border | Dim gray | `#45475A` |
| Background | Terminal default | — |

### Task Status Indicators

| Symbol | State | Color |
|--------|-------|-------|
| `▶` | In progress | Cyan (animated pulse) |
| `✓` | Completed | Green |
| `○` | Pending | Muted gray |
| `✗` | Failed | Red |
| `⏸` | Paused | Yellow |

---

## Main Dashboard View

The primary view showing task list and details side-by-side.

### Running State

```
╭─────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│  chief                                                          ● RUNNING  Iteration 3/40  00:12:34    │
╰─────────────────────────────────────────────────────────────────────────────────────────────────────────╯

╭─ Stories ─────────────────────────────────────╮ ╭─ Details ─────────────────────────────────────────────╮
│                                               │ │                                                       │
│  ✓  US-101  Set up Tailwind CSS with base     │ │  ▶ US-102 · Configure design tokens                   │
│  ▶  US-102  Configure design tokens           │ │                                                       │
│  ○  US-103  Create color theme system         │ │  ─────────────────────────────────────────────────    │
│  ○  US-104  Build Typography component        │ │                                                       │
│  ○  US-105  Create Button component           │ │  As a developer, I need Tailwind configured with      │
│  ○  US-106  Create Card component             │ │  presentation-appropriate design tokens so that       │
│  ○  US-107  Build responsive grid system      │ │  themes can use consistent, large-scale typography    │
│  ○  US-108  Create navigation header          │ │  and spacing values.                                  │
│  ○  US-109  Implement dark mode toggle        │ │                                                       │
│  ○  US-110  Add page transition animations    │ │  ─────────────────────────────────────────────────    │
│  ○  US-111  Create loading skeleton states    │ │                                                       │
│  ○  US-112  Build toast notification system   │ │  Acceptance Criteria                                  │
│                                               │ │                                                       │
│                                               │ │  ○  Extend fontSize scale (slide-sm to slide-hero)    │
│                                               │ │  ○  Extend spacing scale (slide-1 to slide-32)        │
│                                               │ │  ○  Add fontFamily variants (sans, serif, mono)       │
│                                               │ │  ○  Configure custom breakpoints for slides           │
│                                               │ │  ○  Typecheck passes                                  │
│                                               │ │                                                       │
│  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  │ │                                                       │
│  1 of 12 complete                         8%  │ │  Priority P1                                          │
│                                               │ │                                                       │
╰───────────────────────────────────────────────╯ ╰───────────────────────────────────────────────────────╯

╭─ Activity ──────────────────────────────────────────────────────────────────────────────────────────────╮
│  Reading tailwind.config.ts to understand current configuration...                                      │
╰─────────────────────────────────────────────────────────────────────────────────────────────────────────╯

  p Pause   x Stop   t Log   l Switch PRD   ↑↓ Navigate   ? Help                            main   q Quit
```

### Idle State (Ready to Start)

```
╭─────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│  chief                                                                ○ READY  main  12 stories    │
╰─────────────────────────────────────────────────────────────────────────────────────────────────────────╯

╭─ Stories ─────────────────────────────────────╮ ╭─ Details ─────────────────────────────────────────────╮
│                                               │ │                                                       │
│  ○  US-101  Set up Tailwind CSS with base     │ │  ○ US-101 · Set up Tailwind CSS with base config      │
│  ○  US-102  Configure design tokens           │ │                                                       │
│  ○  US-103  Create color theme system         │ │  ─────────────────────────────────────────────────    │
│  ○  US-104  Build Typography component        │ │                                                       │
│  ○  US-105  Create Button component           │ │  As a developer, I need Tailwind CSS installed and    │
│  ○  US-106  Create Card component             │ │  configured with a base setup so that I can start     │
│  ○  US-107  Build responsive grid system      │ │  building components with utility classes.            │
│  ○  US-108  Create navigation header          │ │                                                       │
│  ○  US-109  Implement dark mode toggle        │ │  ─────────────────────────────────────────────────    │
│  ○  US-110  Add page transition animations    │ │                                                       │
│  ○  US-111  Create loading skeleton states    │ │  Acceptance Criteria                                  │
│  ○  US-112  Build toast notification system   │ │                                                       │
│                                               │ │  ○  Install tailwindcss, postcss, autoprefixer        │
│                                               │ │  ○  Create tailwind.config.ts with TypeScript         │
│                                               │ │  ○  Configure content paths for all components        │
│                                               │ │  ○  Add Tailwind directives to global CSS             │
│                                               │ │  ○  Typecheck passes                                  │
│                                               │ │                                                       │
│  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  │ │                                                       │
│  0 of 12 complete                         0%  │ │  Priority P1                                          │
│                                               │ │                                                       │
╰───────────────────────────────────────────────╯ ╰───────────────────────────────────────────────────────╯



  s Start   l Switch PRD   ↑↓ Navigate   ? Help                                             main   q Quit
```

### Paused State

```
╭─────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│  chief                                                         ⏸ PAUSED  Iteration 3/40  00:12:34      │
╰─────────────────────────────────────────────────────────────────────────────────────────────────────────╯

╭─ Stories ─────────────────────────────────────╮ ╭─ Details ─────────────────────────────────────────────╮
│                                               │ │                                                       │
│  ✓  US-101  Set up Tailwind CSS with base     │ │  ⏸ US-102 · Configure design tokens                   │
│  ⏸  US-102  Configure design tokens           │ │                                                       │
│  ○  US-103  Create color theme system         │ │  ─────────────────────────────────────────────────    │
│  ...                                          │ │                                                       │
│                                               │ │  Paused after iteration 3. Press s to resume.         │
│  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  │ │                                                       │
│  1 of 12 complete                         8%  │ │                                                       │
╰───────────────────────────────────────────────╯ ╰───────────────────────────────────────────────────────╯

  s Resume   l Switch PRD   ↑↓ Navigate   ? Help                                            main   q Quit
```

### Complete State

```
╭─────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│  chief                                                       ✓ COMPLETE  12 iterations  00:47:23       │
╰─────────────────────────────────────────────────────────────────────────────────────────────────────────╯

╭─ Stories ─────────────────────────────────────╮ ╭─ Summary ─────────────────────────────────────────────╮
│                                               │ │                                                       │
│  ✓  US-101  Set up Tailwind CSS with base     │ │  ✓ All 12 stories complete!                           │
│  ✓  US-102  Configure design tokens           │ │                                                       │
│  ✓  US-103  Create color theme system         │ │  ─────────────────────────────────────────────────    │
│  ✓  US-104  Build Typography component        │ │                                                       │
│  ✓  US-105  Create Button component           │ │  Duration      47m 23s                                │
│  ✓  US-106  Create Card component             │ │  Iterations    12                                     │
│  ✓  US-107  Build responsive grid system      │ │  Stories       12/12                                  │
│  ✓  US-108  Create navigation header          │ │                                                       │
│  ✓  US-109  Implement dark mode toggle        │ │  ─────────────────────────────────────────────────    │
│  ✓  US-110  Add page transition animations    │ │                                                       │
│  ✓  US-111  Create loading skeleton states    │ │  View progress.md for detailed implementation         │
│  ✓  US-112  Build toast notification system   │ │  notes and learnings.                                 │
│                                               │ │                                                       │
│  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  │ │                                                       │
│  12 of 12 complete                      100%  │ │                                                       │
│                                               │ │                                                       │
╰───────────────────────────────────────────────╯ ╰───────────────────────────────────────────────────────╯

  l Switch PRD   t View Log   ? Help                                                        main   q Quit
```

---

## Log Viewer

Full-screen view showing Claude's streaming output. Toggle with `t` key.

```
╭─────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│  chief                                             ● RUNNING  US-102  Iteration 3/40  00:12:34         │
╰─────────────────────────────────────────────────────────────────────────────────────────────────────────╯

╭─ Log ───────────────────────────────────────────────────────────────────────────────────────────────────╮
│                                                                                                         │
│  Reading prd.json to find the next task to work on...                                                   │
│                                                                                                         │
│  The next story is US-102: Configure design tokens. This story has inProgress: false                    │
│  and passes: false, so I'll start working on it now.                                                    │
│                                                                                                         │
│  First, let me update prd.json to mark this story as in progress.                                       │
│                                                                                                         │
│  ┌─────────────────────────────────────────────────────────────────────────────────────────────────┐    │
│  │  ✏️  Edit  .chief/prds/main/prd.json                                                              │    │
│  └─────────────────────────────────────────────────────────────────────────────────────────────────┘    │
│                                                                                                         │
│  Now let me examine the current Tailwind configuration to understand what's already set up.             │
│                                                                                                         │
│  ┌─────────────────────────────────────────────────────────────────────────────────────────────────┐    │
│  │  📖  Read  tailwind.config.ts                                                                    │    │
│  └─────────────────────────────────────────────────────────────────────────────────────────────────┘    │
│                                                                                                         │
│  The config has a basic setup. I need to extend it with presentation-specific scales.                   │
│  I'll add custom fontSize, spacing, and fontFamily values optimized for slide presentations.            │
│                                                                                                         │
│  ┌─────────────────────────────────────────────────────────────────────────────────────────────────┐    │
│  │  ✏️  Edit  tailwind.config.ts                                                                    │    │
│  └─────────────────────────────────────────────────────────────────────────────────────────────────┘    │
│                                                                                                         │
│  Let me verify the typecheck still passes with these changes.                                           │
│                                                                                                         │
│  ┌─────────────────────────────────────────────────────────────────────────────────────────────────┐    │
│  │  🔨  Bash  npm run typecheck                                                                     │    │
│  └─────────────────────────────────────────────────────────────────────────────────────────────────┘    │
│                                                                                                         │
│  ▌                                                                                                      │
│                                                                                                         │
╰─────────────────────────────────────────────────────────────────────────────────────────────────────────╯

  t Dashboard   p Pause   x Stop   ↑↓ jk Scroll   G Bottom   g Top                          main   q Quit
```

**Tool Icons:**

| Tool | Icon |
|------|------|
| Read | 📖 |
| Edit | ✏️ |
| Write | 📝 |
| Bash | 🔨 |
| Glob | 🔍 |
| Grep | 🔎 |
| Task | 🤖 |
| WebFetch | 🌐 |

---

## PRD Picker

Modal overlay for switching between PRDs. Toggle with `l` key.

```
╭─────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│  chief                                                                  ○ READY  main  12 stories      │
╰─────────────────────────────────────────────────────────────────────────────────────────────────────────╯

        ╭─ Select PRD ────────────────────────────────────────────────────────────────────────╮
        │                                                                                      │
        │   ▶  main                                                            ● Running      │
        │      Tap Documentation Website                                                       │
        │      ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  8/12  67%             │
        │                                                                                      │
        │      api                                                             ○ Ready        │
        │      REST API Refactoring                                                            │
        │      ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  0/18   0%             │
        │                                                                                      │
        │      auth                                                            ⏸ Paused       │
        │      User Authentication System                                                      │
        │      ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  4/12  33%             │
        │                                                                                      │
        │      mobile                                                          ✓ Complete     │
        │      Mobile Responsive Layouts                                                       │
        │      ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  6/6  100%             │
        │                                                                                      │
        ╰──────────────────────────────────────────────────────────────────────────────────────╯

                        ↑↓ Navigate   Enter Select   n New PRD   Esc Back
```

---

## Help Overlay

Modal showing all keyboard shortcuts. Toggle with `?` key.

```
╭─────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│  chief                                                          ● RUNNING  Iteration 3/40  00:12:34    │
╰─────────────────────────────────────────────────────────────────────────────────────────────────────────╯

                ╭─ Keyboard Shortcuts ────────────────────────────────────────────────╮
                │                                                                      │
                │   Loop Control                      Navigation                       │
                │   ────────────                      ──────────                       │
                │   s   Start / Resume                ↑ k   Previous story             │
                │   p   Pause after iteration         ↓ j   Next story                 │
                │   x   Stop immediately              g     Go to top                  │
                │                                     G     Go to bottom               │
                │   Views                                                              │
                │   ─────                             Scrolling (Log View)             │
                │   t   Toggle log view               ─────────────────────            │
                │   l   PRD picker                    Ctrl+D   Page down               │
                │   ?   This help                     Ctrl+U   Page up                 │
                │                                                                      │
                │   General                                                            │
                │   ───────                                                            │
                │   r       Refresh PRD                                                │
                │   q       Quit / Back                                                │
                │   Ctrl+C  Force quit                                                 │
                │                                                                      │
                ╰──────────────────────────────────────────────────────────────────────╯

                                           Esc or ? to close
```

---

## Empty State

Shown when no PRDs exist in the .chief/prds/ directory.

```
╭─────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│  chief                                                                                   No PRD loaded  │
╰─────────────────────────────────────────────────────────────────────────────────────────────────────────╯




                              ╭──────────────────────────────────────────────╮
                              │                                              │
                              │                  ◇                           │
                              │                                              │
                              │         No PRDs found in .chief/prds/        │
                              │                                              │
                              │    Get started by creating a new PRD:        │
                              │                                              │
                              │    $ chief new                              │
                              │      Create a PRD interactively              │
                              │                                              │
                              │    $ chief new "user authentication"        │
                              │      Generate PRD for a specific feature     │
                              │                                              │
                              ╰──────────────────────────────────────────────╯




                                                                                                    q Quit
```

---

## Error State

Shown when an error occurs (e.g., Claude crashes, file not found).

```
╭─────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│  chief                                                            ✗ ERROR  Iteration 3/40  00:12:34    │
╰─────────────────────────────────────────────────────────────────────────────────────────────────────────╯

╭─ Stories ─────────────────────────────────────╮ ╭─ Error ───────────────────────────────────────────────╮
│                                               │ │                                                       │
│  ✓  US-101  Set up Tailwind CSS with base     │ │  ✗ Claude process exited unexpectedly                 │
│  ▶  US-102  Configure design tokens           │ │                                                       │
│  ○  US-103  Create color theme system         │ │  ─────────────────────────────────────────────────    │
│  ○  US-104  Build Typography component        │ │                                                       │
│  ○  US-105  Create Button component           │ │  Exit code: 1                                         │
│  ○  US-106  Create Card component             │ │  Story US-102 was interrupted and will resume         │
│  ○  US-107  Build responsive grid system      │ │  on next iteration.                                   │
│  ○  US-108  Create navigation header          │ │                                                       │
│  ○  US-109  Implement dark mode toggle        │ │  ─────────────────────────────────────────────────    │
│  ○  US-110  Add page transition animations    │ │                                                       │
│  ○  US-111  Create loading skeleton states    │ │  Check claude.log for full error details.             │
│  ○  US-112  Build toast notification system   │ │                                                       │
│                                               │ │                                                       │
│  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  │ │                                                       │
│  1 of 12 complete                         8%  │ │                                                       │
│                                               │ │                                                       │
╰───────────────────────────────────────────────╯ ╰───────────────────────────────────────────────────────╯

  s Retry   t View Log   l Switch PRD   ? Help                                              main   q Quit
```

---

## Interrupted Story Warning

Shown when Chief starts and detects an `inProgress: true` story from a previous session.

```
╭─────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│  chief                                                                 ⚠ INTERRUPTED  main             │
╰─────────────────────────────────────────────────────────────────────────────────────────────────────────╯

╭─ Stories ─────────────────────────────────────╮ ╭─ Notice ──────────────────────────────────────────────╮
│                                               │ │                                                       │
│  ✓  US-101  Set up Tailwind CSS with base     │ │  ⚠ Previous session was interrupted                   │
│  ▶  US-102  Configure design tokens           │ │                                                       │
│  ○  US-103  Create color theme system         │ │  ─────────────────────────────────────────────────    │
│  ...                                          │ │                                                       │
│                                               │ │  Story US-102 has inProgress: true from a             │
│  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  │ │  previous session that didn't complete.               │
│  1 of 12 complete                         8%  │ │                                                       │
│                                               │ │  Press s to resume — the story will be                │
╰───────────────────────────────────────────────╯ │  automatically picked up.                             │
                                                  │                                                       │
                                                  ╰───────────────────────────────────────────────────────╯

  s Resume   l Switch PRD   ↑↓ Navigate   ? Help                                            main   q Quit
```

---

## Narrow Terminal (80 columns)

Graceful degradation for narrower terminals — single column layout.

```
╭──────────────────────────────────────────────────────────────────────────────╮
│  chief                               ● RUNNING  Iteration 3/40  00:12:34    │
╰──────────────────────────────────────────────────────────────────────────────╯

╭─ Stories ────────────────────────────────────────────────────────────────────╮
│                                                                              │
│  ✓  US-101  Set up Tailwind CSS with base config                             │
│  ▶  US-102  Configure design tokens                                          │
│  ○  US-103  Create color theme system                                        │
│  ○  US-104  Build Typography component                                       │
│  ○  US-105  Create Button component                                          │
│  ○  US-106  Create Card component                                            │
│                                                                              │
│  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  1/12  8%    │
│                                                                              │
╰──────────────────────────────────────────────────────────────────────────────╯

╭─ US-102 ─────────────────────────────────────────────────────────────────────╮
│                                                                              │
│  As a developer, I need Tailwind configured with presentation-appropriate    │
│  design tokens so that themes can use consistent, large-scale typography.    │
│                                                                              │
│  ○  Extend fontSize scale (slide-sm to slide-hero)                           │
│  ○  Extend spacing scale (slide-1 to slide-32)                               │
│  ○  Add fontFamily variants                                                  │
│  ○  Typecheck passes                                                         │
│                                                                              │
╰──────────────────────────────────────────────────────────────────────────────╯

  p Pause  x Stop  t Log  l PRD  ↑↓ Nav  ? Help                          q Quit
```

---

**Multiple loops:** Users can run multiple Chief instances on different PRDs in the same project. Each instance is independent. Trust the user to avoid file conflicts between PRDs.

## Keyboard Shortcuts

### Global

| Key | Action |
|-----|--------|
| `q` | Quit / Back |
| `?` | Show help |
| `Ctrl+C` | Force quit |

### Dashboard

| Key | Action |
|-----|--------|
| `s` | Start/resume agent loop |
| `p` | Pause (after current iteration completes) |
| `x` | Stop immediately (kill Claude process) |
| `r` | Refresh (reload PRD file) |
| `l` | Open loop/PRD picker |
| `t` | Toggle log view |
| `↑/k` | Previous task |
| `↓/j` | Next task |
| `Tab` | Switch panel focus |

### Log View

| Key | Action |
|-----|--------|
| `t` | Back to dashboard |
| `f` | Toggle fullscreen |
| `j/↓` | Scroll down |
| `k/↑` | Scroll up |
| `Ctrl+D` | Page down |
| `Ctrl+U` | Page up |
| `G` | Go to bottom |
| `g` | Go to top |

## Notifications

**Completion sound:** A small (~30KB) pleasant chime embedded in the binary, played when user attention is needed:
- All stories complete successfully (`<chief-complete/>` received)
- Max iterations reached (loop stops, user needs to decide next steps)

**Cross-platform playback:**
```go
import "github.com/hajimehoshi/oto/v2"  // Cross-platform audio

//go:embed complete.wav
var completeSound []byte

func playComplete() {
    // Use oto for cross-platform WAV playback
}
```

Sound can be disabled with `--no-sound` flag.

## Embedded Prompts

### Agent Prompt (embed/prompt.txt)

```markdown
# Chief Agent

You are an autonomous agent working through a product requirements document.

## Files

- `.chief/prds/<name>/prd.json` — The PRD with user stories
- `.chief/prds/<name>/progress.md` — Progress log (read Codebase Patterns section first)

## Task

1. Read prd.json and select the next story:
   - FIRST: Any story with `inProgress: true` (resume interrupted work)
   - THEN: Story with lowest `priority` number where `passes: false`
2. Set `inProgress: true` on the selected story in prd.json
3. Implement the story completely
4. Run quality checks (typecheck, lint, test as appropriate)
5. For UI changes, verify in browser using Playwright if available
6. Commit changes using conventional commits (see below)
7. Update prd.json: set `passes: true` and `inProgress: false`
8. Append to progress.md (see format below)

## Conventional Commits

Use this format for all commits:
```
<type>[optional scope]: <description>
```

Types: `feat` (new feature), `fix` (bug fix), `refactor`, `test`, `docs`, `chore`

Examples:
- `feat(auth): add login form validation`
- `fix: prevent race condition in request handler`
- `refactor(api): extract shared validation logic`

Rules:
- Only commit files you modified during this iteration
- Split into multiple commits if logically appropriate
- Never mention Claude or AI in commit messages

## Progress Format

Append to progress.md (never replace):
```
## YYYY-MM-DD - US-XXX: [Title]
- What was implemented
- Files changed
- **Learnings:** (patterns, gotchas, context for future iterations)
---
```

Add reusable patterns to `## Codebase Patterns` at the top of progress.md.

## Completion

After each story, check if ALL stories have `passes: true`.
If complete, output: <chief-complete/>

## Rules

- One story per iteration
- Never commit broken code
- Follow existing code patterns
- Keep changes focused and minimal
```

### PRD Generator Prompt (embed/prd_skill.txt)

Used by `chief new` and `chief edit` - launches an **interactive Claude Code session** with this prompt. The user takes over and collaborates with Claude to build the PRD. Chief just bootstraps the session and exits.

For `chief edit`, the existing `.chief/prd.md` is included as context so Claude can modify it:

```markdown
# PRD Generator

You are helping create a Product Requirements Document.

## Process

1. Ask 3-5 clarifying questions with lettered options (A, B, C, D) about:
   - Problem being solved / goal
   - Core functionality
   - Scope boundaries
   - Success criteria

2. Generate a PRD with:
   - Introduction
   - Goals (measurable)
   - User Stories with acceptance criteria
   - Functional requirements (numbered)
   - Non-Goals (explicit scope boundaries)
   - Design considerations
   - Technical considerations
   - Success metrics
   - Open questions

3. Save to `.chief/prds/<name>/prd.md`

## User Story Format

Each story should be:
- Small enough to complete in ONE Claude context window (one iteration)
- Have specific, verifiable acceptance criteria (not vague)
- Include "Typecheck passes" as criterion
- For UI changes, include "Verify in browser using Playwright"

**Right-sized:** database column addition, single UI component, server action update
**Too large (split these):** complete dashboard, full auth system, API refactor

## Output

Save the PRD as markdown to `.chief/prds/<name>/prd.md`, then inform the user:
"PRD saved to .chief/prds/<name>/prd.md"

(Chief automatically converts to prd.json after this session ends)
```

### PRD Converter Prompt (embed/convert_skill.txt)

Used internally by Chief for auto-conversion. Runs **one-shot** (non-interactive):

```markdown
# PRD Converter

Convert the PRD markdown file to Chief's prd.json format.

## Input

Read the PRD from `.chief/prds/<name>/prd.md`.

## Output Format

```json
{
  "project": "[Project name from PRD]",
  "description": "[Brief description]",
  "userStories": [
    {
      "id": "US-001",
      "title": "[Short title]",
      "description": "[Full story: As a..., I need..., so that...]",
      "acceptanceCriteria": ["Criterion 1", "Criterion 2", "Typecheck passes"],
      "priority": 1,
      "passes": false
    }
  ]
}
```

**Note:** `inProgress` is NOT set here — Claude adds it at runtime.

## Rules

1. **Story sizing**: Each story must complete in ONE iteration (one context window). If describing the change takes more than 2-3 sentences, split it.
2. **Priority order** (lower number = do first): Schema/migrations → Backend/server actions → Frontend/UI → Dashboards/aggregations
3. **Acceptance criteria**: Must be verifiable, not vague. Always include "Typecheck passes". For UI, include "Verify in browser using Playwright".
4. **Dependencies**: No forward dependencies. Story N can only depend on stories 1 to N-1.

## Save

Save to `.chief/prds/<name>/prd.json` and confirm to user.
```

## Data Flow

```
┌──────────────┐     ┌───────────────┐     ┌─────────────┐
│   PRD File   │────▶│  Agent Loop   │────▶│  Progress   │
│  (prd.json)  │◀────│   (Claude)    │     │ (progress.md)
└──────────────┘     └───────────────┘     └─────────────┘
       │                    │
       │  watches for       │  streams
       │  inProgress/passes │  output
       ▼                    ▼
┌─────────────────────────────────────────────────────────┐
│                    TUI (Bubble Tea)                     │
│  ┌─────────┐  ┌─────────────┐  ┌─────────────────────┐  │
│  │ Tasks   │  │   Details   │  │    Log Viewer       │  │
│  │ Panel   │  │   Panel     │  │    (streaming)      │  │
│  └─────────┘  └─────────────┘  └─────────────────────┘  │
└─────────────────────────────────────────────────────────┘
```

**Source of truth:** `prd.json` is the only state file. The TUI reads it to display task status and watches for changes.

## State Management

### Loop States

```go
type LoopState int

const (
    StateReady LoopState = iota    // Waiting to start
    StateRunning                    // Claude is executing
    StatePaused                     // Will stop after current iteration
    StateStopping                   // Stop requested, waiting for Claude
    StateComplete                   // All tasks done
    StateError                      // Something went wrong
)
```

### TUI Model

```go
type Model struct {
    // State (derived from prd.json)
    state        LoopState
    prd          *PRD
    selectedTask int

    // Loop
    iteration    int
    maxIter      int
    claudeCmd    *exec.Cmd

    // Views
    activeView   View  // Dashboard, Log, Picker
    logBuffer    *ring.Buffer

    // Components
    taskList     list.Model
    viewport     viewport.Model
    help         help.Model
}
```

**Note:** All persistent state lives in `prd.json`. The TUI model is ephemeral — if Chief restarts, it re-reads prd.json to determine current status (any story with `inProgress: true` was interrupted).

## Error Handling

### Claude Process Errors

- Detect non-zero exit codes
- Parse error messages from stream-json
- Display in TUI with option to retry or skip
- Log full error context to `claude.log`

### Recovery

- If Claude crashes mid-story, `inProgress` stays true in prd.json
- Next iteration automatically resumes the interrupted story (prompt prioritizes `inProgress: true`)
- Failed iterations still count toward max-iterations limit
- TUI shows warning: "Story US-XXX was interrupted — resuming"

### File System Errors

- Handle missing prd.json gracefully (show picker or init prompt)
- Auto-create progress.md if missing
- Watch for external file changes (hot reload PRD)

## Distribution

### Build Targets

```bash
# Via goreleaser
goreleaser release --snapshot --clean
```

Targets:
- darwin/amd64
- darwin/arm64
- linux/amd64
- linux/arm64
- windows/amd64

### Installation Methods

```bash
# Homebrew (macOS/Linux)
brew install chief

# Go install
go install github.com/minicodemonkey/chief@latest

# Download binary
curl -fsSL https://raw.githubusercontent.com/MiniCodeMonkey/chief/refs/heads/main/install.sh | sh
```

## Implementation Phases

### Phase 1: Core

- [ ] Go project setup with Bubble Tea
- [ ] Embedded agent prompt
- [ ] Core loop (~80 lines)
- [ ] Stream-json parser
- [ ] Basic dashboard view (task list + details)
- [ ] Start/pause/stop controls
- [ ] PRD file watching

### Phase 2: Full TUI

- [ ] Pretty log viewer with tool cards
- [ ] PRD picker for multiple loops
- [ ] Progress bar component
- [ ] Keyboard navigation
- [ ] Help overlay

### Phase 3: PRD Generation

- [ ] `chief new` command (launches interactive Claude session with embedded prompt)
- [ ] `chief edit` command (launches interactive session with existing PRD as context)
- [ ] Auto-conversion logic (prd.md → prd.json with progress protection)
- [ ] Merge behavior for preserving story status
- [ ] Embedded skill prompts

### Phase 4: Polish

- [ ] Completion sound (embedded WAV)
- [ ] Error recovery UX
- [ ] `chief status` quick command
- [ ] `chief list` quick command

### Phase 5: Distribution

- [ ] goreleaser config
- [ ] Homebrew formula
- [ ] Install script
- [ ] README and docs

## Testing Strategy

### Unit Tests

**Parser tests** (`internal/loop/parser_test.go`):
```go
func TestParseLine_AssistantText(t *testing.T) {
    line := `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello"}]}}`
    event := parseLine(line)
    assert.Equal(t, AssistantText, event.Type)
    assert.Equal(t, "Hello", event.Text)
}

func TestParseLine_ToolUse(t *testing.T) {
    line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"foo.txt"}}]}}`
    event := parseLine(line)
    assert.Equal(t, ToolStart, event.Type)
    assert.Equal(t, "Read", event.Tool)
}

func TestParseLine_ChiefComplete(t *testing.T) {
    line := `{"type":"assistant","message":{"content":[{"type":"text","text":"All done! <chief-complete/>"}]}}`
    event := parseLine(line)
    assert.Equal(t, Complete, event.Type)
}
```

**PRD tests** (`internal/prd/loader_test.go`):
```go
func TestLoadPRD(t *testing.T) { ... }
func TestPRD_AllComplete(t *testing.T) { ... }
func TestPRD_NextStory_PrioritizesInProgress(t *testing.T) { ... }
func TestPRD_NextStory_LowestPriority(t *testing.T) { ... }
```

**Auto-conversion tests** (`internal/prd/convert_test.go`):
```go
func TestNeedsConversion_NoJSON(t *testing.T) { ... }
func TestNeedsConversion_MDNewer(t *testing.T) { ... }
func TestNeedsConversion_JSONNewer(t *testing.T) { ... }
func TestMergeProgress_MatchingIDs(t *testing.T) { ... }
func TestMergeProgress_NewStories(t *testing.T) { ... }
func TestMergeProgress_RemovedStories(t *testing.T) { ... }
func TestHasProgress_Empty(t *testing.T) { ... }
func TestHasProgress_WithPasses(t *testing.T) { ... }
func TestHasProgress_WithInProgress(t *testing.T) { ... }
```

**TUI tests** (`internal/tui/app_test.go`):
```go
// Bubble Tea provides teatest for TUI testing
func TestDashboard_KeyboardNavigation(t *testing.T) {
    m := NewModel(testPRD)
    m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
    assert.Equal(t, 1, m.selectedTask)
}

func TestDashboard_StartStopControls(t *testing.T) { ... }
func TestLogView_Scrolling(t *testing.T) { ... }
```

### Integration Tests

**Loop integration** (`internal/loop/loop_test.go`):
```go
func TestLoop_MockClaude(t *testing.T) {
    // Create a mock "claude" script that outputs predefined stream-json
    mockClaude := createMockClaude(t, []string{
        `{"type":"assistant","message":{"content":[{"type":"text","text":"Working..."}]}}`,
        `{"type":"result","result":"Done"}`,
    })
    defer mockClaude.Cleanup()

    loop := NewLoop(testPRDPath, WithClaudePath(mockClaude.Path))
    events := collectEvents(loop.Run(context.Background()))

    assert.Contains(t, events, Event{Type: AssistantText, Text: "Working..."})
}
```

**File watching** (`internal/prd/watcher_test.go`):
```go
func TestWatcher_DetectsChanges(t *testing.T) {
    // Write prd.json, start watcher, modify file, verify event
}
```

### End-to-End Tests

**E2E with real Claude** (`e2e/e2e_test.go`):
```go
// +build e2e

func TestE2E_SingleStory(t *testing.T) {
    // Requires ANTHROPIC_API_KEY
    // Uses a minimal test PRD with one trivial story
    // Verifies: story completes, prd.json updated, progress.md written
}
```

Run E2E tests explicitly: `go test -tags=e2e ./e2e/...`

### Test Fixtures

```
testdata/
├── prds/
│   ├── valid.json              # Well-formed PRD
│   ├── partial_progress.json   # PRD with some stories complete
│   ├── all_complete.json       # PRD with all stories complete
│   ├── in_progress.json        # PRD with interrupted story
│   └── invalid.json            # Malformed JSON
├── stream/
│   ├── simple_story.jsonl      # Mock Claude output for one story
│   ├── tool_calls.jsonl        # Output with various tool uses
│   ├── error_exit.jsonl        # Output ending in error
│   └── complete.jsonl          # Output with <chief-complete/>
└── markdown/
    ├── simple.md               # Simple PRD markdown
    └── complex.md              # PRD with many stories
```

### CI Pipeline

```yaml
# .github/workflows/test.yml
name: Test
on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - run: go test -race -coverprofile=coverage.out ./...
      - run: go build ./cmd/chief

  e2e:
    runs-on: ubuntu-latest
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
      - run: go test -tags=e2e ./e2e/...
    env:
      ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
```

### What We Don't Test

- Claude's behavior (that's Anthropic's job)
- Actual file edits made by Claude (too flaky, too slow)
- Sound playback (manual verification)
- Complex TUI interactions (manual verification, use teatest for basics)

## Documentation

### README.md Structure

```markdown
# Chief 👮

Autonomous agent loop for working through PRDs with Claude Code.

*Named after Chief Wiggum, Ralph Wiggum's dad from The Simpsons.*

## Quick Start

\`\`\`bash
# Install
brew install chief

# Create a PRD interactively
chief new

# Run the agent loop
chief
\`\`\`

## How It Works

Chief orchestrates Claude Code to work through user stories autonomously:

1. You write a PRD describing what you want built
2. Chief converts it to machine-readable format
3. Claude works through each story, one at a time
4. You watch progress in a beautiful TUI

[Diagram: PRD → Chief Loop → Claude → Code Changes → Repeat]

## Installation

[brew, go install, binary download]

## Usage

### Creating a PRD

\`\`\`bash
chief new                    # Interactive PRD creation
chief new auth               # Create PRD named "auth"
chief new auth "OAuth login" # With initial context
chief edit                    # Edit existing PRD
\`\`\`

### Running the Loop

\`\`\`bash
chief                         # Run default PRD
chief auth                    # Run specific PRD
chief --max-iterations 20     # Limit iterations
\`\`\`

### Keyboard Controls

| Key | Action |
|-----|--------|
| s | Start/Resume |
| p | Pause |
| x | Stop |
| t | Toggle log view |
| ? | Help |
| q | Quit |

## PRD Format

[Link to detailed PRD format docs]

## Configuration

[CLI flags only, no config file]

## Troubleshooting

[Common issues and solutions]

## License

MIT
```

### Inline Code Documentation

**Every public function gets a doc comment:**
```go
// Run executes the agent loop until all stories are complete or max iterations
// is reached. It spawns Claude as a subprocess and streams output to the TUI
// via the events channel. The loop can be paused with Pause() or stopped
// immediately with Stop().
//
// Run blocks until the loop completes. Check the returned error and the final
// event to determine why the loop ended (Complete, MaxIterationsReached, or Error).
func (l *Loop) Run(ctx context.Context) error {
```

**Complex logic gets inline comments:**
```go
// parseLine extracts events from Claude's stream-json output.
// The format is one JSON object per line with these types:
//   - "assistant": Claude's response (text or tool_use)
//   - "tool_result": Result of a tool call
//   - "result": Final result of the conversation
func (l *Loop) parseLine(line string) *Event {
```

### Architecture Decision Records (ADRs)

Store in `docs/adr/`:

```markdown
# ADR-001: Go + Bubble Tea for TUI

## Status
Accepted

## Context
We need a cross-platform TUI with single-binary distribution...

## Decision
Use Go with the Bubble Tea framework...

## Consequences
- Pro: Single binary, easy distribution
- Pro: Excellent TUI ecosystem
- Con: More verbose than Python/Node alternatives
```

Key ADRs to write:
- ADR-001: Go + Bubble Tea for TUI
- ADR-002: prd.md as source of truth (auto-conversion)
- ADR-003: Stream-json for Claude output parsing
- ADR-004: Single iteration = single Claude invocation
- ADR-005: No branch management (keep it simple)

### Man Page

Generate from README using `ronn` or similar:
```bash
chief(1)                    Chief Manual                    chief(1)

NAME
       chief - autonomous agent loop for PRDs

SYNOPSIS
       chief [options] [prd-name]
       chief new [name] [context]
       chief edit [name]
       chief status [name]
       chief list

DESCRIPTION
       Chief orchestrates Claude Code to work through product
       requirements documents autonomously...
```

## Future Enhancements (Post-MVP)

- Subagent monitoring (track Task tool spawns)
- Cost tracking (parse API usage from stream-json)
- Git integration (show commits made during session)
- Diff preview (show pending changes)
- Web UI (optional browser-based dashboard)
- Team mode (multiple users watching same session)
