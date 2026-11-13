package plugin

import (
	"testing"
	"time"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

func TestConvertToolCallInfo(t *testing.T) {
	call := model.ToolCall{ID: "t1", Name: "fs__read_file", Arguments: []byte(`{"path":"a.go"}`)}
	tool := toolrouter.Tool{Server: "fs", Risk: adlcv1.RiskClass_RISK_CLASS_WRITE_LOCAL}

	t.Run("pre", func(t *testing.T) {
		got := toProtoInfo(call, tool)
		if got.GetCall().GetId() != "t1" || got.GetCall().GetName() != "fs__read_file" ||
			got.GetCall().GetArgumentsJson() != `{"path":"a.go"}` {
			t.Errorf("call = %v", got.GetCall())
		}
		if got.GetRiskClass() != adlcv1.RiskClass_RISK_CLASS_WRITE_LOCAL || got.GetServer() != "fs" {
			t.Errorf("risk/server = %v/%q", got.GetRiskClass(), got.GetServer())
		}
	})
	t.Run("post", func(t *testing.T) {
		res := toolrouter.Result{Output: []byte("out"), IsError: true, ExitCode: 2, Duration: 1500 * time.Millisecond}
		got := toPostToolRequest(call, res)
		if got.GetInfo().GetCall().GetId() != "t1" || !got.GetIsError() || string(got.GetOutput()) != "out" ||
			got.GetExitCode() != 2 || got.GetDuration().AsDuration() != 1500*time.Millisecond {
			t.Errorf("request = %v", got)
		}
	})
}
