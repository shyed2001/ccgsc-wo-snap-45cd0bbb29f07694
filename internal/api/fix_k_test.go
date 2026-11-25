package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"ccgsc/poolscore/internal/pool"
)

func TestFIXKResultViewUsesConformanceShape(t *testing.T) {
	result := resultView(pool.Result{
		JobID:  "s1.tp.j1",
		TaskID: "t1",
		Status: "failed",
		At:     time.Unix(10, 0).UTC(),
		Error:  "worker failed",
	})
	if result["jobId"] != "s1.tp.j1" || result["taskId"] != "t1" {
		t.Fatalf("identity fields = %#v", result)
	}
	errView, ok := result["error"].(map[string]any)
	if !ok || errView["code"] != "task-failed" || errView["status"] != http.StatusInternalServerError {
		t.Fatalf("error view = %#v", result["error"])
	}
	if result["completedAt"] == nil || result["late"] != false {
		t.Fatalf("completion fields = %#v", result)
	}
}

func TestFIXKResultViewPreservesTaskOutputEnvelope(t *testing.T) {
	result := resultView(pool.Result{
		Output: json.RawMessage(`{"json":{"answer":42}}`),
	})
	output, ok := result["output"].(map[string]any)
	if !ok || output["json"] == nil {
		t.Fatalf("output = %#v", result["output"])
	}
}

func TestFIXKRawObjectOrNil(t *testing.T) {
	if rawObjectOrNil(nil) != nil {
		t.Fatal("empty raw object should be nil")
	}
	value := rawObjectOrNil(json.RawMessage(`{"mode":"tab"}`))
	object, ok := value.(map[string]any)
	if !ok || object["mode"] != "tab" {
		t.Fatalf("object = %#v", value)
	}
}
