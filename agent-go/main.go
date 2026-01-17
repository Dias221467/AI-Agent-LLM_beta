package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const systemPrompt = `
You are an autonomous AI agent that operates a web browser using tools.

You are NOT a chatbot.
You do NOT provide explanations unless explicitly asked.

Your objective is to complete the user’s task autonomously by:
- observing the browser state,
- deciding the next best action,
- calling exactly one tool per step,
- analyzing the result,
- repeating until completion.

Rules:
- Do NOT hardcode URLs, selectors, button names, or page structures.
- Infer actions only from the provided observation.
- Do NOT assume prior knowledge of any website.
- Ask the user only if essential information is missing.

Error handling:
- If an action fails or does not progress the task, adapt your strategy.
- Do not repeat the same failing action more than twice.

Completion:
- If the goal is achieved or no further progress is possible, call finish().

Available tools:
- navigate(url)
- click(element_id)
- type(element_id, text)
- scroll(direction)
- wait(milliseconds)
- ask_user(question)
- finish(summary)

Output rules (CRITICAL):
- Respond with EXACTLY ONE valid JSON object.
- Do NOT include explanations, markdown, or any text outside JSON.

Response formats:

Tool call:
{"action":"tool","tool":"<tool_name>","arguments":{...}}

Ask user:
{"action":"ask_user","question":"<question>"}

Finish:
{"action":"finish","summary":"<short text>","results":[{"job_title":"...","company_name":"..."}]}
- summary MUST be a string.
- results MUST be an array of objects with job_title and company_name.
- Do NOT put arrays/objects inside summary.
`

type WorkerCmd struct {
	Action string                 `json:"action"`
	Args   map[string]interface{} `json:"args"`
}

type WorkerResp struct {
	Status      string                 `json:"status"`
	Message     string                 `json:"message,omitempty"`
	Observation map[string]interface{} `json:"observation,omitempty"`
}

type PyWorker struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
}

func StartPyWorker() (*PyWorker, error) {
	cmd := exec.Command("python", "worker.py")
	cmd.Dir = "../browser-worker"
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	worker := &PyWorker{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdoutPipe),
	}

	// Read first line: worker_started
	line, err := worker.stdout.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSpace(line)

	var hello WorkerResp
	if err := json.Unmarshal([]byte(line), &hello); err != nil {
		return nil, fmt.Errorf("failed to parse worker hello: %v, line=%s", err, line)
	}
	if hello.Status != "ok" {
		return nil, fmt.Errorf("worker did not start: %s", hello.Message)
	}

	fmt.Println("✅ Python worker started")
	return worker, nil
}

func (w *PyWorker) Send(action string, args map[string]interface{}) (WorkerResp, error) {
	cmdObj := WorkerCmd{
		Action: action,
		Args:   args,
	}
	data, _ := json.Marshal(cmdObj)

	_, err := w.stdin.Write(append(data, '\n'))
	if err != nil {
		return WorkerResp{}, err
	}

	line, err := w.stdout.ReadString('\n')
	if err != nil {
		return WorkerResp{}, err
	}
	line = strings.TrimSpace(line)

	var resp WorkerResp
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return WorkerResp{}, fmt.Errorf("failed to parse worker response: %v, line=%s", err, line)
	}
	return resp, nil
}

func (w *PyWorker) Stop() {
	_, _ = w.Send("exit", map[string]interface{}{})
	_ = w.cmd.Process.Kill()
}

type AgentAction struct {
	Action    string                 `json:"action"`
	Tool      string                 `json:"tool,omitempty"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
	Question  string                 `json:"question,omitempty"`

	// summary может прийти строкой или массивом (из-за модели)
	SummaryRaw json.RawMessage `json:"summary,omitempty"`

	// нормальный структурированный результат
	Results []JobItem `json:"results,omitempty"`

	Summary string `json:"-"`
}

type JobItem struct {
	JobTitle    string `json:"job_title"`
	CompanyName string `json:"company_name"`
}

func fakeLLM(task string, observation map[string]interface{}) AgentAction {
	url, _ := observation["url"].(string)

	if url == "" {
		return AgentAction{
			Action:    "tool",
			Tool:      "navigate",
			Arguments: map[string]interface{}{"url": "https://hh.ru"},
		}
	}

	return AgentAction{
		Action:  "finish",
		Summary: "Opened hh.ru successfully",
	}
}

type Listing struct {
	Title   string
	Company string
}

func normalizeSpaces(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ") // NBSP
	s = strings.TrimSpace(s)
	// сжать пробелы
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}

func isLikelyCompany(s string) bool {
	s = normalizeSpaces(s)
	if s == "" {
		return false
	}
	low := strings.ToLower(s)

	// отсекаем строки НЕ про конкретную вакансию
	badTitle := []string{
		"активных вакансий",
		"сейчас смотрят",
		"актуальные вакансии",
		"все вакансии",
		"вакансии компании",
	}
	for _, b := range badTitle {
		if strings.Contains(low, b) {
			return false
		}
	}

	// отсекаем мусор/фильтры/зарплаты/города
	bad := []string{
		"руб", "₽", "kzt", "тенге", "₸",
		"сегодня", "вчера", "отклик", "показать", "сортировка",
		"вакансии", "вакансия", "похожие", "результаты", "найдено",
		"удаленно", "удалённо", "remote",
		"опыт", "график", "занятость", "формат работы", "удаленная работа", "удалённая работа", "полная занятость", "частичная занятость",
		"выплаты", "зарплата", "з/п", "оклад", "доход", "на руки", "gross", "net", "в месяц", "в неделю", "в день", "за смену", "за проект",
		"два раза в месяц", "еженедельно", "ежедневно", "график", "занятость", "формат", "удобный график", "полная занятость", "частичная занятость", "проектная работа",
		"стажировка", "адрес", "метро", "город", "на карте", "сменный", "вахта", "подработка",
	}
	for _, b := range bad {
		if strings.Contains(low, b) {
			return false
		}
	}

	// компания обычно не слишком длинная
	if len([]rune(s)) > 60 {
		return false
	}

	return true
}

func isLikelyTitle(s string) bool {
	s = normalizeSpaces(s)
	if s == "" {
		return false
	}
	low := strings.ToLower(s)

	// не вакансия
	badTitle := []string{
		"найдено", "вакансий", "по соответствию",
		"сохранить поиск", "вакансии на карте",
		"исключить слова", "уровень дохода", "опыт",
	}
	for _, b := range badTitle {
		if strings.Contains(low, b) {
			return false
		}
	}

	mustHaveRole := []string{
		"engineer", "инженер", "developer", "разработчик", "scientist", "аналитик", "data engineer",
	}
	if !(strings.Contains(low, "engineer") || strings.Contains(low, "инженер")) {
		return false
	}
	roleOk := false
	for _, k := range mustHaveRole {
		if strings.Contains(low, k) {
			roleOk = true
			break
		}
	}
	if !roleOk {
		return false
	}
	// отсекаем очевидные не-заголовки
	bad := []string{
		"войти", "регистрация", "резюме", "отклик", "подписаться",
		"показать", "фильтр", "сортировка",
	}
	for _, b := range bad {
		if strings.Contains(low, b) {
			return false
		}
	}

	// заголовок обычно 4..90 символов
	n := len([]rune(s))
	return n >= 4 && n <= 90
}

func ExtractListingsFromVisibleText(visible string, want int) []Listing {
	visible = strings.ReplaceAll(visible, "\r\n", "\n")

	// Ищем начало блока с результатами ("Найдено XX вакансий")
	lower := strings.ToLower(visible)
	if idx := strings.Index(lower, "найдено"); idx >= 0 {
		visible = visible[idx:]
	}

	linesRaw := strings.Split(visible, "\n")

	// чистим линии
	lines := make([]string, 0, len(linesRaw))
	for _, l := range linesRaw {
		l = normalizeSpaces(l)
		if l == "" {
			continue
		}
		if len([]rune(l)) > 120 {
			continue
		}
		lines = append(lines, l)
	}

	found := make([]Listing, 0, want)
	seen := map[string]bool{}

	for i := 0; i < len(lines); i++ {
		title := lines[i]

		// cразу отсекаем мусорные "заголовки"
		lowTitle := strings.ToLower(title)
		badTitle := []string{
			"найдено", "вакансий", "по соответствию",
			"сохранить поиск", "вакансии на карте",
			"исключить слова", "уровень дохода", "опыт",
		}
		skip := false
		for _, b := range badTitle {
			if strings.Contains(lowTitle, b) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		if !isLikelyTitle(title) {
			continue
		}

		// компания обычно рядом ниже
		company := ""
		for j := i + 1; j < len(lines) && j <= i+6; j++ {
			if isLikelyCompany(lines[j]) {
				company = lines[j]
				break
			}
		}

		if company == "" {
			continue
		}

		key := strings.ToLower(title) + "|" + strings.ToLower(company)
		if seen[key] {
			continue
		}
		seen[key] = true

		found = append(found, Listing{
			Title:   title,
			Company: company,
		})

		if len(found) >= want {
			break
		}
	}

	return found
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`

	Error *struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

func callGemini(systemPrompt, userPrompt string) (string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("GEMINI_API_KEY is not set")
	}

	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", model, apiKey)

	payload := map[string]interface{}{
		"systemInstruction": map[string]interface{}{
			"parts": []map[string]string{
				{"text": systemPrompt},
			},
		},
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]string{
					{"text": userPrompt},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"temperature":      0,
			"maxOutputTokens":  800,
			"responseMimeType": "application/json",
		},
	}

	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}

	if out.Error != nil {
		return "", fmt.Errorf("Gemini API error: %s (%s)", out.Error.Message, out.Error.Status)
	}

	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty Gemini response")
	}

	return strings.TrimSpace(out.Candidates[0].Content.Parts[0].Text), nil
}

func normalizeAction(a *AgentAction, raw string) error {
	toolNames := map[string]bool{
		"navigate":    true,
		"click":       true,
		"type":        true,
		"scroll":      true,
		"wait":        true,
		"observe":     true,
		"press_enter": true,
	}

	// Case 1: model returned {"action":"click", ...}
	if toolNames[a.Action] {
		if a.Tool == "" {
			a.Tool = a.Action
		}
		a.Action = "tool"
	}

	// Case 2: model returned {"action":"tool"} but forgot tool field
	if a.Action == "tool" && a.Tool == "" {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &m); err == nil {
			// tool может быть в поле "tool"
			if t, ok := m["tool"].(string); ok {
				a.Tool = t
			}
			// или tool может быть в поле "action"
			if a.Tool == "" {
				if t, ok := m["action"].(string); ok && toolNames[t] {
					a.Tool = t
				}
			}

			if a.Arguments == nil {
				a.Arguments = map[string]interface{}{}
			}
			if args, ok := m["args"].(map[string]interface{}); ok {
				for k, v := range args {
					a.Arguments[k] = v
				}
			}
			// если модель положила параметры прямо в корень
			for k, v := range m {
				if k == "action" || k == "tool" || k == "arguments" || k == "args" || k == "question" || k == "summary" {
					continue
				}
				if _, exists := a.Arguments[k]; !exists {
					a.Arguments[k] = v
				}
			}
		}
	}

	return nil
}

func finalizeActionFields(a *AgentAction) {
	if len(a.SummaryRaw) == 0 {
		return
	}

	var s string
	if err := json.Unmarshal(a.SummaryRaw, &s); err == nil {
		a.Summary = s
		return
	}

	var arr []JobItem
	if err := json.Unmarshal(a.SummaryRaw, &arr); err == nil {
		if len(a.Results) == 0 {
			a.Results = arr
		}
		a.Summary = fmt.Sprintf("Found %d jobs.", len(arr))
		return
	}

	a.Summary = "Done."
}

func isSearchResultsReady(observation map[string]interface{}) bool {
	u, _ := observation["url"].(string)
	uLow := strings.ToLower(u)

	// мы точно на странице результатов поиска
	if !strings.Contains(uLow, "/search/vacancy") {
		return false
	}

	if !strings.Contains(uLow, "text=") {
		return false
	}

	if strings.Contains(uLow, "text=ai") || strings.Contains(uLow, "text=ai%20engineer") || strings.Contains(uLow, "ai%20engineer") {
		return true
	}

	return false
}

func obsSignature(obs map[string]interface{}) string {
	u, _ := obs["url"].(string)
	t, _ := obs["title"].(string)
	vt, _ := obs["visible_text"].(string)

	head := vt
	if len(head) > 300 {
		head = head[:300]
	}
	return u + "|" + t + "|" + head
}

func decideNextAction(task string, observation map[string]interface{}) (AgentAction, error) {
	promptObj := map[string]interface{}{
		"task":        strings.TrimSpace(task),
		"observation": observation,
	}

	promptBytes, _ := json.MarshalIndent(promptObj, "", "  ")
	userPrompt := string(promptBytes)

	raw, err := callGemini(systemPrompt, userPrompt)
	if err != nil {
		return AgentAction{}, err
	}

	raw = strings.TrimSpace(raw)

	if !strings.HasPrefix(raw, "{") {
		if i := strings.Index(raw, "{"); i >= 0 {
			if j := strings.LastIndex(raw, "}"); j > i {
				raw = raw[i : j+1]
			}
		}
	}

	var action AgentAction
	if err := json.Unmarshal([]byte(raw), &action); err != nil {
		return AgentAction{}, fmt.Errorf("failed to parse LLM JSON: %v\nraw=%s", err, raw)
	}

	_ = normalizeAction(&action, raw)
	finalizeActionFields(&action)
	return action, nil
}

func main() {
	worker, err := StartPyWorker()
	if err != nil {
		panic(err)
	}
	defer worker.Stop()

	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Enter task: ")
	task, _ := reader.ReadString('\n')

	var observation map[string]interface{}

	const MaxSteps = 15
	var lastAction string
	var sameActionCount int

	var lastObsSig string
	var stagnantCount int

	searchSubmitted := false
	printedListings := false
	switchedToMoscow := false

	for step := 1; step <= MaxSteps; step++ {
		fmt.Printf("\n--- STEP %d ---\n", step)

		action, err := decideNextAction(task, observation)
		if err != nil {
			msg := err.Error()

			// Gemini rate limit / quota: ждём и повторяем шаг
			if strings.Contains(msg, "RESOURCE_EXHAUSTED") ||
				strings.Contains(msg, "Quota exceeded") ||
				strings.Contains(msg, "Please retry in") {

				wait := 60 * time.Second
				if idx := strings.Index(msg, "Please retry in "); idx >= 0 {
					tail := msg[idx+len("Please retry in "):]
					// tail начинается примерно с "56.25s"
					if sIdx := strings.Index(tail, "s"); sIdx > 0 {
						numStr := strings.TrimSpace(tail[:sIdx])
						if f, perr := strconv.ParseFloat(numStr, 64); perr == nil {
							// + небольшой буфер
							wait = time.Duration(int(f)+2) * time.Second
						}
					}
				}

				fmt.Printf("Gemini rate limit. Waiting %v then retrying...\n", wait)
				time.Sleep(wait)

				step--
				continue
			}

			fmt.Println("LLM error:", err)
			break
		}
		fmt.Printf("LLM raw action: action=%s tool=%s args=%v question=%q summary=%q\n",
			action.Action, action.Tool, action.Arguments, action.Question, action.Summary)

		// loop-protection
		cur := action.Action
		if action.Action == "tool" {
			cur += ":" + action.Tool

			// не считаем scroll повтором
			if action.Tool == "scroll" {
				cur += ":" + fmt.Sprint(time.Now().UnixNano())
			}
		}
		if cur == lastAction {
			sameActionCount++
		} else {
			sameActionCount = 0
		}
		lastAction = cur

		if sameActionCount >= 2 {
			fmt.Println("⚠️ Detected repeated action, stopping.")
			break
		}

		switch action.Action {
		case "finish":
			fmt.Println("✅ DONE:", action.Summary)

			// если LLM вернул структурированный список
			if len(action.Results) > 0 {
				for i, it := range action.Results {
					fmt.Printf("%d) %s — %s\n", i+1, it.JobTitle, it.CompanyName)
				}
			}
			return

		case "ask_user":
			fmt.Println("?", action.Question)
			answer, _ := reader.ReadString('\n')
			task = task + "\nUser answer: " + answer
			continue

		case "tool":
			if action.Tool == "" {
				fmt.Println("LLM returned tool action without tool name")
				continue
			}
			if action.Arguments == nil {
				action.Arguments = map[string]interface{}{}
			}

			resp, err := worker.Send(action.Tool, action.Arguments)
			if err != nil {
				fmt.Println("Worker send error:", err)
				continue
			}
			if resp.Status == "error" {
				fmt.Println("Worker error:", resp.Message)
				continue
			}
			observation = resp.Observation

			//
			sig := obsSignature(observation)
			if sig == lastObsSig {
				stagnantCount++
			} else {
				stagnantCount = 0
			}
			lastObsSig = sig

			if stagnantCount >= 2 {
				// пробуем сдвинуть страницу вместо бесконечных повторов кликов/ввода
				_, _ = worker.Send("scroll", map[string]interface{}{"direction": "down"})
				time.Sleep(800 * time.Millisecond)
				continue
			}
			//ANTI-SPAM

			if isSearchResultsReady(observation) {
				searchSubmitted = true
			}
			// считаем, что поиск выполнен, если:
			// 1) был click (кнопка поиска)
			// 2) или мы уже на URL с text=
			if u, ok := observation["url"].(string); ok {
				if strings.Contains(u, "text=") && strings.Contains(strings.ToLower(u), "ai") {
					searchSubmitted = true
				}
			}
			//AUTOCOLLECT
			if searchSubmitted && !printedListings {
				if vt, ok := observation["visible_text"].(string); ok && vt != "" {
					listings := ExtractListingsFromVisibleText(vt, 3)
					if len(listings) > 0 {
						printedListings = true

						fmt.Println("\n📌 Found listings:")
						for idx, it := range listings {
							fmt.Printf("%d) %s — %s\n", idx+1, it.Title, it.Company)
						}

						if len(listings) >= 3 {
							fmt.Println("\n✅ DONE: collected 3 listings.")
							return
						}
					}
				}
			}

			// fallback если явно "ничего не найдено"
			if vt, ok := observation["visible_text"].(string); ok {
				low := strings.ToLower(vt)
				if !switchedToMoscow &&
					(strings.Contains(low, "ничего не найдено") || strings.Contains(low, "не найдено")) {

					switchedToMoscow = true
					fmt.Println("⚠️ No results detected, switching to Moscow (area=1) ...")

					_, _ = worker.Send("navigate", map[string]interface{}{
						"url": "https://hh.ru/search/vacancy?text=AI%20Engineer&area=1",
					})
				}
			}
			time.Sleep(3 * time.Second)

		default:
			fmt.Println("Unknown action from LLM:", action.Action)
		}
	}

	fmt.Println("⚠️ Stopped: max steps reached or agent halted.")
}
