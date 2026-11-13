package modeltest

import (
	"errors"
	"fmt"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// fakeTB records failures instead of failing the test.
type fakeTB struct {
	testing.TB
	errs []string
}

func (f *fakeTB) Helper() {}
func (f *fakeTB) Errorf(format string, args ...any) {
	f.errs = append(f.errs, fmt.Sprintf(format, args...))
}
func (f *fakeTB) Fatalf(format string, args ...any) { f.Errorf(format, args...) }

func TestContractAgainstFake(t *testing.T) {
	Run(t, func(t *testing.T, sc Scenario) model.Adapter {
		if sc.WantErr != nil {
			return model.NewFake(model.FakeTurn{Err: sc.WantErr})
		}
		return model.NewFake(model.FakeTurn{Response: sc.Want})
	})
}

func responseWithArgs(args string) *model.Response {
	return &model.Response{Message: model.Message{Role: model.RoleAssistant, Parts: []model.Part{
		model.ToolCallPart(model.ToolCall{ID: "1", Name: "a", Arguments: []byte(args)}),
	}}}
}

func TestCheckResponseIgnoresArgumentFormatting(t *testing.T) {
	tb := &fakeTB{}
	CheckResponse(tb, responseWithArgs(`{"a":1,"b":2}`), responseWithArgs(`{ "b":2, "a":1 }`))
	if len(tb.errs) != 0 {
		t.Fatalf("unexpected failures: %v", tb.errs)
	}
	CheckResponse(tb, responseWithArgs(`{"a":1}`), responseWithArgs(`{"a":2}`))
	if len(tb.errs) == 0 {
		t.Fatal("different arguments did not fail")
	}
}

func TestCheckErrorDetectsMismatch(t *testing.T) {
	want := &model.Error{Class: adlcv1.ErrorClass_ERROR_CLASS_MODEL, HTTPStatus: 500, Retryable: true}
	tb := &fakeTB{}
	CheckError(tb, fmt.Errorf("wrap: %w", want), want)
	if len(tb.errs) != 0 {
		t.Fatalf("unexpected failures: %v", tb.errs)
	}
	CheckError(tb, errors.New("plain"), want)
	CheckError(tb, &model.Error{Class: want.Class, HTTPStatus: 400}, want)
	if len(tb.errs) != 2 {
		t.Fatalf("got %d failures, want 2", len(tb.errs))
	}
}
