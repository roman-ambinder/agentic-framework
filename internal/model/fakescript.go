package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

type scriptFile struct {
	Turns []json.RawMessage `json:"turns"`
}

type scriptTurn struct {
	Text      string           `json:"text"`
	ToolCalls []scriptToolCall `json:"tool_calls"`
	Finish    string           `json:"finish"`
	Usage     *scriptUsage     `json:"usage"`
	Error     *scriptError     `json:"error"`
}

type scriptToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type scriptUsage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

type scriptError struct {
	Class      string `json:"class"`
	HTTPStatus int    `json:"http_status"`
	Retryable  bool   `json:"retryable"`
	Message    string `json:"message"`
}

var toolNameRE = regexp.MustCompile(ToolNamePattern)

var scriptFinish = map[string]adlcv1.FinishReason{
	"end_turn":   adlcv1.FinishReason_FINISH_REASON_END_TURN,
	"tool_use":   adlcv1.FinishReason_FINISH_REASON_TOOL_USE,
	"max_tokens": adlcv1.FinishReason_FINISH_REASON_MAX_TOKENS,
	"refusal":    adlcv1.FinishReason_FINISH_REASON_REFUSAL,
	"paused":     adlcv1.FinishReason_FINISH_REASON_PAUSED,
	"other":      adlcv1.FinishReason_FINISH_REASON_OTHER,
}

var scriptClass = map[string]adlcv1.ErrorClass{
	"model":      adlcv1.ErrorClass_ERROR_CLASS_MODEL,
	"rate_limit": adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT,
	"timeout":    adlcv1.ErrorClass_ERROR_CLASS_TIMEOUT,
	"internal":   adlcv1.ErrorClass_ERROR_CLASS_INTERNAL,
}

// ParseFakeScript decodes a fake script. Unknown fields are an error.
func ParseFakeScript(data []byte) ([]FakeTurn, error) {
	var file scriptFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("model: parse fake script: %w", err)
	}
	if dec.More() {
		return nil, errors.New("model: parse fake script: trailing data after object")
	}
	if len(file.Turns) == 0 {
		return nil, errors.New("model: fake script has no turns")
	}
	turns := make([]FakeTurn, len(file.Turns))
	for i, raw := range file.Turns {
		turn, err := parseTurn(raw)
		if err != nil {
			return nil, fmt.Errorf("model: fake script turn %d: %w", i, err)
		}
		turns[i] = turn
	}
	return turns, nil
}

// parseTurn strictly decodes one turn, so an unknown field is reported with
// the turn's index by the caller.
func parseTurn(raw json.RawMessage) (FakeTurn, error) {
	var st scriptTurn
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		return FakeTurn{}, err
	}
	return st.build()
}

// LoadFakeScript reads the file at path and calls ParseFakeScript.
func LoadFakeScript(path string) ([]FakeTurn, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("model: read fake script: %w", err)
	}
	return ParseFakeScript(data)
}

func (st scriptTurn) build() (FakeTurn, error) {
	hasContent := st.Text != "" || len(st.ToolCalls) > 0
	if st.Error != nil {
		if hasContent || st.Finish != "" || st.Usage != nil {
			return FakeTurn{}, errors.New("error cannot be combined with text, tool_calls, finish or usage")
		}
		return st.buildError()
	}
	finish, err := st.finishReason()
	if err != nil {
		return FakeTurn{}, err
	}
	if !hasContent && finish != adlcv1.FinishReason_FINISH_REASON_REFUSAL {
		return FakeTurn{}, errors.New("needs error, text or tool_calls")
	}
	msg := Message{Role: RoleAssistant}
	if st.Text != "" {
		msg.Parts = append(msg.Parts, TextPart(st.Text))
	}
	for j, tc := range st.ToolCalls {
		call, err := tc.build()
		if err != nil {
			return FakeTurn{}, fmt.Errorf("tool call %d: %w", j, err)
		}
		msg.Parts = append(msg.Parts, ToolCallPart(call))
	}
	resp := &Response{Message: msg, Finish: finish, ProviderFinish: st.finishName()}
	if st.Usage != nil {
		resp.Usage = Usage(*st.Usage)
	}
	return FakeTurn{Response: resp}, nil
}

func (st scriptTurn) finishName() string {
	if st.Finish != "" {
		return st.Finish
	}
	if len(st.ToolCalls) > 0 {
		return "tool_use"
	}
	return "end_turn"
}

func (st scriptTurn) finishReason() (adlcv1.FinishReason, error) {
	f, ok := scriptFinish[st.finishName()]
	if !ok {
		return 0, fmt.Errorf("unknown finish %q", st.Finish)
	}
	return f, nil
}

func (st scriptTurn) buildError() (FakeTurn, error) {
	class, ok := scriptClass[st.Error.Class]
	if !ok {
		return FakeTurn{}, fmt.Errorf("unknown error class %q", st.Error.Class)
	}
	return FakeTurn{Err: &Error{
		Class:      class,
		HTTPStatus: st.Error.HTTPStatus,
		Retryable:  st.Error.Retryable,
		Err:        errors.New(st.Error.Message),
	}}, nil
}

func (tc scriptToolCall) build() (ToolCall, error) {
	if tc.ID == "" || tc.Name == "" {
		return ToolCall{}, errors.New("id and name are required")
	}
	if !toolNameRE.MatchString(tc.Name) {
		return ToolCall{}, fmt.Errorf("tool name %q does not match %s", tc.Name, ToolNamePattern)
	}
	args := tc.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil || obj == nil {
		return ToolCall{}, errors.New("arguments must be a JSON object")
	}
	return ToolCall{ID: tc.ID, Name: tc.Name, Arguments: append(json.RawMessage(nil), args...)}, nil
}
