# Browser AI Agent

Autonomous AI agent that controls a real web browser to solve multi-step tasks.
The agent receives a task in text form, opens a browser, analyzes the page, and decides what to do next step by step.

The project demonstrates autonomous decision-making, browser automation, and context-aware control without hardcoded selectors or predefined flows.

---

## What this project does

- Opens a real Chromium browser
- Accepts a free-form task from the terminal
- Autonomously navigates websites
- Clicks, types, scrolls, and waits when needed
- Extracts information from pages
- Stops when the task is completed or no progress is possible

Example task:
```
On https://hh.ru find 3 AI Engineer jobs and extract job title and company name.
```

---

## Key properties

- **Autonomous agent** – no predefined step-by-step scripts
- **No hardcoded selectors or URLs**
- **Context-aware** – decisions are based only on current page state
- **Loop protection** – prevents infinite clicking or typing
- **Robust parsing** – tolerant to imperfect LLM output
- **Real browser** – powered by Playwright (Chromium)

---

## Tech stack

- Go – agent logic, LLM communication, safety logic
- Python – browser worker
- Playwright – browser automation
- Gemini API – decision-making LLM

---

## Project structure

```
browser-ai-agent/
├── agent-go/
│   ├── go.mod
│   └── main.go
├── browser-worker/
│   ├── worker.py
│   └── requirements.txt
├── venv/
├── .gitignore
└── README.md
```

---

## Setup (Windows)

### 1. Create and activate virtual environment

```
python -m venv venv
.\venv\Scripts\Activate.ps1
```

### 2. Install Python dependencies

```
pip install -r .\browser-worker\requirements.txt
python -m playwright install chromium
```

### 3. Set Gemini API key

```
$env:GEMINI_API_KEY="YOUR_API_KEY"
```

Optional:
```
$env:GEMINI_MODEL="gemini-2.5-flash"
```

---

## Run

From the project root:

```
go run .\agent-go\main.go
```

Then enter a task in the terminal and watch the agent work in the browser.

---

## Notes

- No login or registration is performed by the agent
- API keys are read from environment variables
- The `venv` directory should not be committed

---

## Purpose

This project was created as a demonstration of:
- autonomous browser agents
- tool-based LLM control
- context-driven decision making
- safe and adaptable automation without hardcoded flows
