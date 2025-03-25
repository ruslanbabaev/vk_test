package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/tarantool/go-tarantool"
)

type Vote struct {
	ID        string
	CreatorID string
	Question  string
	Options   []string
	Active    bool
	CreatedAt time.Time
}

type VoteResult struct {
	VoteID string
	UserID string
	Option string
}

type VoteHandler struct {
	tarantoolConn *tarantool.Connection
}

type MattermostRequest struct {
	Text      string `json:"text"`
	UserID    string `json:"user_id"`
	ChannelID string `json:"channel_id"`
}

type MattermostResponse struct {
	ResponseType string        `json:"response_type"`
	Text         string        `json:"text"`
	Attachments  []interface{} `json:"attachments,omitempty"`
}

func main() {
	// Подключение к Tarantool
	conn, err := tarantool.Connect("localhost:3301", tarantool.Opts{
		User: "admin",
		Pass: "password",
	})
	if err != nil {
		log.Fatalf("Failed to connect to Tarantool: %v", err)
	}
	defer conn.Close()

	// Инициализация пространств
	initSpaces(conn)

	handler := VoteHandler{tarantoolConn: conn}

	http.HandleFunc("/create", handler.CreateVote)
	http.HandleFunc("/vote", handler.SubmitVote)
	http.HandleFunc("/results", handler.GetResults)
	http.HandleFunc("/end", handler.EndVote)
	http.HandleFunc("/delete", handler.DeleteVote)

	log.Println("Starting vote bot server on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func initSpaces(conn *tarantool.Connection) {
	// Создаем пространство для голосований
	_, err := conn.Exec(tarantool.Eval(`
		box.schema.create_space('votes', {
			if_not_exists = true,
			format = {
				{name = 'id', type = 'string'},
				{name = 'creator_id', type = 'string'},
				{name = 'question', type = 'string'},
				{name = 'options', type = 'array'},
				{name = 'active', type = 'boolean'},
				{name = 'created_at', type = 'unsigned'}
			}
		})
	`, []interface{}{}))
	if err != nil {
		log.Printf("Error creating votes space: %v", err)
	}

	_, err = conn.Exec(tarantool.Eval(`
		box.space.votes:create_index('primary', {
			type = 'tree',
			parts = {'id'},
			if_not_exists = true
		})
	`, []interface{}{}))
	if err != nil {
		log.Printf("Error creating votes index: %v", err)
	}

	// Создаем пространство для результатов
	_, err = conn.Exec(tarantool.Eval(`
		box.schema.create_space('vote_results', {
			if_not_exists = true,
			format = {
				{name = 'id', type = 'string'},
				{name = 'vote_id', type = 'string'},
				{name = 'user_id', type = 'string'},
				{name = 'option', type = 'string'},
				{name = 'voted_at', type = 'unsigned'}
			}
		})
	`, []interface{}{}))
	if err != nil {
		log.Printf("Error creating vote_results space: %v", err)
	}

	_, err = conn.Exec(tarantool.Eval(`
		box.space.vote_results:create_index('primary', {
			type = 'tree',
			parts = {'id'},
			if_not_exists = true
		})
	`, []interface{}{}))
	if err != nil {
		log.Printf("Error creating vote_results index: %v", err)
	}

	_, err = conn.Exec(tarantool.Eval(`
		box.space.vote_results:create_index('vote_user', {
			type = 'tree',
			parts = {'vote_id', 'user_id'},
			if_not_exists = true,
			unique = true
		})
	`, []interface{}{}))
	if err != nil {
		log.Printf("Error creating vote_user index: %v", err)
	}
}

func (h *VoteHandler) CreateVote(w http.ResponseWriter, r *http.Request) {
	var req MattermostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(req.Text, " ", 2)
	if len(parts) < 2 {
		sendMattermostResponse(w, "Usage: /create \"Question\" [\"Option1\", \"Option2\", ...]", nil)
		return
	}

	var question string
	var options []string

	// Парсинг вопроса и вариантов
	if err := json.Unmarshal([]byte(parts[1]), &question); err == nil {
		// Если передан только вопрос
		options = []string{"Да", "Нет"}
	} else {
		// Пытаемся распарсить как массив [вопрос, варианты]
		var data []interface{}
		if err := json.Unmarshal([]byte(parts[1]), &data); err != nil || len(data) < 1 {
			sendMattermostResponse(w, "Invalid format. Use: /create \"Question\" or /create [\"Question\", \"Option1\", \"Option2\"]", nil)
			return
		}

		question = fmt.Sprintf("%v", data[0])
		if len(data) > 1 {
			options = make([]string, len(data)-1)
			for i, opt := range data[1:] {
				options[i] = fmt.Sprintf("%v", opt)
			}
		} else {
			options = []string{"Да", "Нет"}
		}
	}

	voteID := generateID()
	vote := Vote{
		ID:        voteID,
		CreatorID: req.UserID,
		Question:  question,
		Options:   options,
		Active:    true,
		CreatedAt: time.Now(),
	}

	_, err := h.tarantoolConn.Insert("votes", []interface{}{
		vote.ID,
		vote.CreatorID,
		vote.Question,
		vote.Options,
		vote.Active,
		vote.CreatedAt.Unix(),
	})

	if err != nil {
		log.Printf("Error creating vote: %v", err)
		sendMattermostResponse(w, "Failed to create vote", nil)
		return
	}

	// Формируем варианты для отображения в Mattermost
	var attachments []interface{}
	for i, option := range vote.Options {
		attachments = append(attachments, map[string]interface{}{
			"text":    fmt.Sprintf("%d. %s", i+1, option),
			"color":   "#00FF00",
			"actions": []map[string]interface{}{},
		})
	}

	response := fmt.Sprintf("Голосование создано!\n*Вопрос:* %s\n*ID голосования:* %s\n*Варианты ответа:*", vote.Question, vote.ID)
	sendMattermostResponse(w, response, attachments)
}

func (h *VoteHandler) SubmitVote(w http.ResponseWriter, r *http.Request) {
	var req MattermostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(req.Text, " ", 3)
	if len(parts) < 3 {
		sendMattermostResponse(w, "Usage: /vote <vote_id> <option_number>", nil)
		return
	}

	voteID := parts[1]
	optionNum := parts[2]

	// Проверяем существование голосования
	resp, err := h.tarantoolConn.Select("votes", "primary", 0, 1, tarantool.IterEq, []interface{}{voteID})
	if err != nil || len(resp.Data) == 0 {
		sendMattermostResponse(w, "Голосование не найдено", nil)
		return
	}

	vote := resp.Data[0].([]interface{})
	if !vote[4].(bool) {
		sendMattermostResponse(w, "Голосование завершено", nil)
		return
	}

	options := vote[3].([]interface{})
	optionIndex := 0
	if _, err := fmt.Sscanf(optionNum, "%d", &optionIndex); err != nil || optionIndex < 1 || optionIndex > len(options) {
		sendMattermostResponse(w, fmt.Sprintf("Неверный номер варианта. Допустимые значения: 1-%d", len(options)), nil)
		return
	}

	selectedOption := options[optionIndex-1].(string)

	// Проверяем, не голосовал ли уже пользователь
	resp, err = h.tarantoolConn.Select("vote_results", "vote_user", 0, 1, tarantool.IterEq, []interface{}{voteID, req.UserID})
	if err == nil && len(resp.Data) > 0 {
		sendMattermostResponse(w, "Вы уже проголосовали в этом голосовании", nil)
		return
	}

	// Сохраняем голос
	resultID := fmt.Sprintf("%s_%s", voteID, req.UserID)
	_, err = h.tarantoolConn.Insert("vote_results", []interface{}{
		resultID,
		voteID,
		req.UserID,
		selectedOption,
		time.Now().Unix(),
	})

	if err != nil {
		log.Printf("Error submitting vote: %v", err)
		sendMattermostResponse(w, "Failed to submit vote", nil)
		return
	}

	sendMattermostResponse(w, fmt.Sprintf("Ваш голос за вариант \"%s\" принят", selectedOption), nil)
}

func (h *VoteHandler) GetResults(w http.ResponseWriter, r *http.Request) {
	var req MattermostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(req.Text, " ", 2)
	if len(parts) < 2 {
		sendMattermostResponse(w, "Usage: /results <vote_id>", nil)
		return
	}

	voteID := parts[1]

	// Получаем информацию о голосовании
	resp, err := h.tarantoolConn.Select("votes", "primary", 0, 1, tarantool.IterEq, []interface{}{voteID})
	if err != nil || len(resp.Data) == 0 {
		sendMattermostResponse(w, "Голосование не найдено", nil)
		return
	}

	vote := resp.Data[0].([]interface{})
	question := vote[2].(string)
	options := vote[3].([]interface{})
	isActive := vote[4].(bool)

	// Получаем результаты
	resp, err = h.tarantoolConn.Select("vote_results", "primary", 0, 0, tarantool.IterGe, []interface{}{voteID})
	if err != nil {
		log.Printf("Error getting results: %v", err)
		sendMattermostResponse(w, "Failed to get results", nil)
		return
	}

	// Считаем голоса
	votesCount := make(map[string]int)
	for _, opt := range options {
		votesCount[opt.(string)] = 0
	}

	totalVotes := 0
	for _, item := range resp.Data {
		result := item.([]interface{})
		option := result[3].(string)
		votesCount[option]++
		totalVotes++
	}

	// Формируем ответ
	var resultText strings.Builder
	resultText.WriteString(fmt.Sprintf("*Результаты голосования:* %s\n", question))
	resultText.WriteString(fmt.Sprintf("*Статус:* %s\n", map[bool]string{true: "Активно", false: "Завершено"}[isActive]))
	resultText.WriteString(fmt.Sprintf("*Всего голосов:* %d\n\n", totalVotes))

	for i, opt := range options {
		option := opt.(string)
		count := votesCount[option]
		percent := 0
		if totalVotes > 0 {
			percent = count * 100 / totalVotes
		}
		resultText.WriteString(fmt.Sprintf("%d. %s: %d голосов (%d%%)\n", i+1, option, count, percent))
	}

	sendMattermostResponse(w, resultText.String(), nil)
}

func (h *VoteHandler) EndVote(w http.ResponseWriter, r *http.Request) {
	var req MattermostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(req.Text, " ", 2)
	if len(parts) < 2 {
		sendMattermostResponse(w, "Usage: /end <vote_id>", nil)
		return
	}

	voteID := parts[1]

	// Проверяем существование голосования и что пользователь - создатель
	resp, err := h.tarantoolConn.Select("votes", "primary", 0, 1, tarantool.IterEq, []interface{}{voteID})
	if err != nil || len(resp.Data) == 0 {
		sendMattermostResponse(w, "Голосование не найдено", nil)
		return
	}

	vote := resp.Data[0].([]interface{})
	if vote[1].(string) != req.UserID {
		sendMattermostResponse(w, "Только создатель может завершить голосование", nil)
		return
	}

	// Обновляем статус голосования
	_, err = h.tarantoolConn.Update("votes", "primary", []interface{}{voteID}, []interface{}{
		[]interface{}{"=", 4, false},
	})

	if err != nil {
		log.Printf("Error ending vote: %v", err)
		sendMattermostResponse(w, "Failed to end vote", nil)
		return
	}

	sendMattermostResponse(w, "Голосование завершено", nil)
}

func (h *VoteHandler) DeleteVote(w http.ResponseWriter, r *http.Request) {
	var req MattermostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(req.Text, " ", 2)
	if len(parts) < 2 {
		sendMattermostResponse(w, "Usage: /delete <vote_id>", nil)
		return
	}

	voteID := parts[1]

	// Проверяем существование голосования и что пользователь - создатель
	resp, err := h.tarantoolConn.Select("votes", "primary", 0, 1, tarantool.IterEq, []interface{}{voteID})
	if err != nil || len(resp.Data) == 0 {
		sendMattermostResponse(w, "Голосование не найдено", nil)
		return
	}

	vote := resp.Data[0].([]interface{})
	if vote[1].(string) != req.UserID {
		sendMattermostResponse(w, "Только создатель может удалить голосование", nil)
		return
	}

	// Удаляем голосование и результаты
	_, err = h.tarantoolConn.Delete("votes", "primary", []interface{}{voteID})
	if err != nil {
		log.Printf("Error deleting vote: %v", err)
		sendMattermostResponse(w, "Failed to delete vote", nil)
		return
	}

	// Удаляем все результаты для этого голосования
	_, err = h.tarantoolConn.Call("box.space.vote_results.index.primary:pairs", []interface{}{
		voteID, {iterator = "GE"}, {iterator = "LE"},
	})
	if err == nil {
		// В реальном коде нужно реализовать пакетное удаление
	}

	sendMattermostResponse(w, "Голосование удалено", nil)
}

func sendMattermostResponse(w http.ResponseWriter, text string, attachments []interface{}) {
	response := MattermostResponse{
		ResponseType: "in_channel",
		Text:         text,
		Attachments:  attachments,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func generateID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())
}