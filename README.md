# Chief

Build big projects with AI. Chief breaks your work into tasks and runs an agent loop until they're done.

## Usage
```bash
# Create a new project
chief new

# Launch the TUI and press 's' to start
chief
```

Chief runs an AI-powered agent in a [Ralph Wiggum loop](https://ghuntley.com/ralph/): each iteration starts with a fresh context window, but progress is persisted between runs. This lets the agent work through large projects without hitting context limits. One commit per task keeps your git history clean and easy to review.

## Backends

| Backend | Setup |
|--------|-------|
| **Docker** | `docker run ...` — batteries included, no extra install |
| **Ollama** | [ollama.com](https://ollama.com/) running locally with a tool-supporting model (e.g. `qwen2.5-coder:32b`) |

## How It Works

1. **Describe your project** as a series of tasks
2. **Chief runs an agent loop**, one task at a time
3. **One commit per task** — clean git history, easy to review

## License CC04