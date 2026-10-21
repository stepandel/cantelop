package jsonwire

import (
	"encoding/json"
	"testing"
)

func TestMarshalWithRawFieldPreservesPayload(t *testing.T) {
	for _, payload := range []string{
		`{"text":"</script>&","literal":"\\u2028","quote":"\"","number":9007199254740993}`,
		"{\"text\":\"\u2028\u2029\"}",
		`"string"`, `null`, `[true,1e100]`,
	} {
		document, err := MarshalWithRawField(struct {
			ID string `json:"id"`
		}{ID: "safe\"}payload"}, "data", json.RawMessage(payload))
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			ID   string          `json:"id"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(document, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.ID != "safe\"}payload" || string(decoded.Data) != payload {
			t.Fatalf("JSON values changed: %s", document)
		}
	}
}

func TestMarshalWithRawFieldRejectsInvalidJSON(t *testing.T) {
	for _, payload := range []string{"", `{`, `null,"injected":true`, `{} {}`} {
		if _, err := MarshalWithRawField(struct{}{}, "data", json.RawMessage(payload)); err == nil {
			t.Fatalf("accepted invalid payload %q", payload)
		}
	}
	if _, err := MarshalWithRawField([]string{}, "data", json.RawMessage(`null`)); err == nil {
		t.Fatal("accepted non-object envelope")
	}
}
