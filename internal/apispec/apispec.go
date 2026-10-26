// Package apispec embeds the client REST contract (openapi.yaml of fin-assist/contracts)
// and exposes the operations bank-bff serves for request validation.
package apispec

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers"
)

// Source records the fin-assist/contracts commit the embedded copy was taken from.
const Source = "fin-assist/contracts 8b3b1b639220941462bbabc28208cb1eb00ef0fc openapi/openapi.yaml"

//go:embed openapi.yaml
var document []byte

// Document returns a copy of the embedded contract bytes.
func Document() []byte { return append([]byte(nil), document...) }

// Spec is the parsed contract. It is safe for concurrent use after Load.
type Spec struct{ doc *openapi3.T }

// Load parses and validates the embedded contract once per process; the parsed
// document is immutable and shared. Embedded examples are not validated: two
// examples of the source commit are incomplete (FIN-339), which does not affect
// schema validation of requests and responses.
func Load(context.Context) (*Spec, error) { return loadOnce() }

var loadOnce = sync.OnceValues(func() (*Spec, error) { return parse(context.Background(), document) })

func parse(ctx context.Context, data []byte) (*Spec, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(data)
	if err != nil {
		return nil, fmt.Errorf("openapi: parse: %w", err)
	}
	if err := doc.Validate(ctx, openapi3.DisableExamplesValidation()); err != nil {
		return nil, fmt.Errorf("openapi: validate: %w", err)
	}
	return &Spec{doc: doc}, nil
}

// Operation is one served contract operation.
type Operation struct {
	// Route is what openapi3filter needs to validate a request or a response.
	Route *routers.Route
	// Statuses are the HTTP statuses the operation declares.
	Statuses map[int]bool
}

// ErrUnknownOperation is returned for a path and method absent from the contract.
var ErrUnknownOperation = errors.New("openapi: operation not in contract")

// Operation returns the operation of a path template ("/v1/consents/{type}") and method.
func (s *Spec) Operation(method, path string) (Operation, error) {
	item := s.doc.Paths.Value(path)
	if item == nil {
		return Operation{}, ErrUnknownOperation
	}
	op := item.GetOperation(method)
	if op == nil {
		return Operation{}, ErrUnknownOperation
	}
	statuses := make(map[int]bool)
	for code := range op.Responses.Map() {
		if n, err := strconv.Atoi(code); err == nil {
			statuses[n] = true
		}
	}
	return Operation{
		Route:    &routers.Route{Spec: s.doc, Path: path, PathItem: item, Method: method, Operation: op},
		Statuses: statuses,
	}, nil
}
