package apispec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestUnitEmbeddedContractServesTheOperations(t *testing.T) {
	s, err := Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Load(context.Background())
	if again != s {
		t.Fatal("the parsed contract is shared")
	}
	op, err := s.Operation("POST", "/v1/assistant/messages")
	if err != nil || !op.Statuses[201] || !op.Statuses[429] || op.Statuses[404] || op.Route.Operation.OperationID != "askAssistant" || op.Route.Path != "/v1/assistant/messages" {
		t.Fatalf("%v %+v", err, op.Statuses)
	}
	for _, tc := range [][2]string{{"GET", "/v1/nowhere"}, {"PATCH", "/v1/assistant/limit"}} {
		if _, err := s.Operation(tc[0], tc[1]); !errors.Is(err, ErrUnknownOperation) {
			t.Fatalf("%v: %v", tc, err)
		}
	}
	doc := Document()
	doc[0] = 'x'
	if bytes.Equal(doc, document) {
		t.Fatal("Document returns a copy")
	}
}

func TestUnitInvalidContractIsRefused(t *testing.T) {
	for _, data := range []string{"{not yaml", "openapi: 3.0.3\ninfo: {}\npaths: {}\n"} {
		if _, err := parse(context.Background(), []byte(data)); err == nil {
			t.Fatalf("%q accepted", data)
		}
	}
}

func TestUnitContractValidatesExamplesAndDefaults(t *testing.T) {
	for _, field := range []string{"example", "default"} {
		for _, valid := range []bool{false, true} {
			value := "42"
			if valid {
				value = "hello"
			}
			raw := fmt.Sprintf("openapi: 3.0.3\ninfo: {title: probe, version: 1.0.0}\npaths: {}\ncomponents:\n  schemas:\n    Probe:\n      type: string\n      %s: %s\n", field, value)
			_, err := parse(context.Background(), []byte(raw))
			if (err == nil) != valid {
				t.Fatalf("%s valid%v: %v", field, valid, err)
			}
		}
	}
}
