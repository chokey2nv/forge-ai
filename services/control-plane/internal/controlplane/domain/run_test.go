package domain

import (
	"encoding/json"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// The adapter posts checkpoints as JSON, they are stored as BSON, and resumes
// send them back as JSON. Assistant tool calls must survive the whole trip.
func TestCheckpointKeepsToolCalls(t *testing.T) {
	in := `{"messages":[
		{"role":"assistant","content":"","toolCalls":[{"id":"toolu_1","name":"read_file","arguments":{"path":"src/a.js"}}]},
		{"role":"tool","content":"...","toolCallId":"toolu_1","toolName":"read_file"}
	]}`
	var cp Checkpoint
	if err := json.Unmarshal([]byte(in), &cp); err != nil {
		t.Fatal(err)
	}

	raw, err := bson.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	var stored Checkpoint
	if err := bson.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}

	out, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Messages []struct {
			ToolCalls []struct {
				ID        string         `json:"id"`
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"toolCalls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	calls := back.Messages[0].ToolCalls
	if len(calls) != 1 || calls[0].ID != "toolu_1" || calls[0].Name != "read_file" || calls[0].Arguments["path"] != "src/a.js" {
		t.Fatalf("tool calls lost in round trip: %s", out)
	}
}
