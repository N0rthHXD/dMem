package longmemeval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ChatModel interface {
	Complete(context.Context, string, int) (ChatResult, error)
	Model() string
}

type ChatResult struct {
	Content          string `json:"content"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
}

type OpenAIChatModel struct {
	http    *http.Client
	baseURL string
	apiKey  string
	model   string
}

func NewOpenAIChatModel(baseURL, apiKey, model string, timeout time.Duration) (*OpenAIChatModel, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if strings.TrimSpace(apiKey) == "" || strings.TrimSpace(model) == "" || timeout <= 0 {
		return nil, errors.New("API key, model, and positive timeout are required")
	}
	return &OpenAIChatModel{http: &http.Client{Timeout: timeout}, baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, model: model}, nil
}

func (m *OpenAIChatModel) Model() string { return m.model }

func (m *OpenAIChatModel) Complete(ctx context.Context, prompt string, maxTokens int) (ChatResult, error) {
	body := struct {
		Model       string `json:"model"`
		Messages    []any  `json:"messages"`
		Temperature int    `json:"temperature"`
		MaxTokens   int    `json:"max_tokens"`
	}{Model: m.model, Messages: []any{map[string]string{"role": "user", "content": prompt}}, MaxTokens: maxTokens}
	data, err := json.Marshal(body)
	if err != nil {
		return ChatResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return ChatResult{}, err
	}
	request.Header.Set("Authorization", "Bearer "+m.apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := m.http.Do(request)
	if err != nil {
		return ChatResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return ChatResult{}, fmt.Errorf("chat completion returned %d: %s", response.StatusCode, message)
	}
	var value struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(&value); err != nil {
		return ChatResult{}, fmt.Errorf("decode chat completion: %w", err)
	}
	if len(value.Choices) == 0 || strings.TrimSpace(value.Choices[0].Message.Content) == "" {
		return ChatResult{}, errors.New("chat completion has no content")
	}
	return ChatResult{Content: strings.TrimSpace(value.Choices[0].Message.Content), PromptTokens: value.Usage.PromptTokens, CompletionTokens: value.Usage.CompletionTokens, RequestID: value.ID}, nil
}

func answerPrompt(item Case, memories []datedMemory) string {
	var history strings.Builder
	for _, item := range memories {
		fmt.Fprintf(&history, "Session Date: %s\n%s\n\n", item.Date.Format(DateLayout), item.Content)
	}
	return fmt.Sprintf("I will give you several history chats between you and a user. Please answer the question based on the relevant chat history.\n\n\nHistory Chats:\n\n%sCurrent Date: %s\nQuestion: %s\nAnswer:", history.String(), item.QuestionDate, item.Question)
}

func judgePrompt(item Case, hypothesis string) string {
	var template string
	if item.IsAbstention() {
		template = "I will give you an unanswerable question, an explanation, and a response from a model. Please answer yes if the model correctly identifies the question as unanswerable. The model could say that the information is incomplete, or some other information is given but the asked information is not.\n\nQuestion: %s\n\nExplanation: %s\n\nModel Response: %s\n\nDoes the model correctly identify the question as unanswerable? Answer yes or no only."
	} else {
		switch item.QuestionType {
		case "single-session-user", "single-session-assistant", "multi-session":
			template = "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no. If the response is equivalent to the correct answer or contains all the intermediate steps to get the correct answer, you should also answer yes. If the response only contains a subset of the information required by the answer, answer no. \n\nQuestion: %s\n\nCorrect Answer: %s\n\nModel Response: %s\n\nIs the model response correct? Answer yes or no only."
		case "temporal-reasoning":
			template = "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no. If the response is equivalent to the correct answer or contains all the intermediate steps to get the correct answer, you should also answer yes. If the response only contains a subset of the information required by the answer, answer no. In addition, do not penalize off-by-one errors for the number of days. If the question asks for the number of days/weeks/months, etc., and the model makes off-by-one errors (e.g., predicting 19 days when the answer is 18), the model's response is still correct. \n\nQuestion: %s\n\nCorrect Answer: %s\n\nModel Response: %s\n\nIs the model response correct? Answer yes or no only."
		case "knowledge-update":
			template = "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no. If the response contains some previous information along with an updated answer, the response should be considered as correct as long as the updated answer is the required answer.\n\nQuestion: %s\n\nCorrect Answer: %s\n\nModel Response: %s\n\nIs the model response correct? Answer yes or no only."
		case "single-session-preference":
			template = "I will give you a question, a rubric for desired personalized response, and a response from a model. Please answer yes if the response satisfies the desired response. Otherwise, answer no. The model does not need to reflect all the points in the rubric. The response is correct as long as it recalls and utilizes the user's personal information correctly.\n\nQuestion: %s\n\nRubric: %s\n\nModel Response: %s\n\nIs the model response correct? Answer yes or no only."
		}
	}
	return fmt.Sprintf(template, item.Question, item.Answer, hypothesis)
}
