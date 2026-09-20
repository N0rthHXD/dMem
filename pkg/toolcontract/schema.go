package toolcontract

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema/v1/*.json
var schemaFiles embed.FS

const schemaBase = "https://dmem.dev/schemas/agent-tool/v1/"

const (
	MaxRecallDocument   = 256 << 10
	MaxRememberDocument = 1 << 20
	MaxFeedbackDocument = 64 << 10
	MaxForgetDocument   = 64 << 10
	MaxGenericDocument  = 256 << 10
)

var schemaNames = []string{
	SchemaCapabilities, SchemaError, SchemaRecallInput, SchemaRecallOutput,
	SchemaRememberInput, SchemaRememberOutput, SchemaFeedbackInput,
	SchemaFeedbackOutput, SchemaForgetInput, SchemaForgetOutput,
}

type compiledSchemas struct {
	values map[string]*jsonschema.Schema
	err    error
}

var schemasOnce = sync.OnceValue(func() compiledSchemas {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	for _, name := range schemaNames {
		data, err := schemaFiles.ReadFile("schema/v1/" + name + ".json")
		if err != nil {
			return compiledSchemas{err: err}
		}
		var document any
		if err := json.Unmarshal(data, &document); err != nil {
			return compiledSchemas{err: err}
		}
		if err := compiler.AddResource(schemaBase+name+".json", document); err != nil {
			return compiledSchemas{err: err}
		}
	}
	values := make(map[string]*jsonschema.Schema, len(schemaNames))
	for _, name := range schemaNames {
		schema, err := compiler.Compile(schemaBase + name + ".json")
		if err != nil {
			return compiledSchemas{err: err}
		}
		values[name] = schema
	}
	return compiledSchemas{values: values}
})

func SchemaNames() []string {
	result := append([]string(nil), schemaNames...)
	sort.Strings(result)
	return result
}

func Schema(name string) ([]byte, error) {
	if !knownSchema(name) {
		return nil, fmt.Errorf("toolcontract: unknown schema %q", name)
	}
	return schemaFiles.ReadFile("schema/v1/" + name + ".json")
}

func Validate(name string, document []byte) error {
	compiled := schemasOnce()
	if compiled.err != nil {
		return fmt.Errorf("toolcontract: compile schemas: %w", compiled.err)
	}
	schema, ok := compiled.values[name]
	if !ok {
		return fmt.Errorf("toolcontract: unknown schema %q", name)
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("toolcontract: invalid JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("toolcontract: document must contain one JSON value")
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("toolcontract: schema validation: %w", err)
	}
	return nil
}

func DecodeStrict(name string, document []byte, destination any) error {
	if len(document) > DocumentLimit(name) {
		return fmt.Errorf("toolcontract: document exceeds size limit")
	}
	if err := Validate(name, document); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("toolcontract: decode: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("toolcontract: document must contain one JSON value")
	}
	return nil
}

func DocumentLimit(name string) int {
	switch name {
	case SchemaRememberInput:
		return MaxRememberDocument
	case SchemaFeedbackInput:
		return MaxFeedbackDocument
	case SchemaForgetInput:
		return MaxForgetDocument
	case SchemaRecallInput:
		return MaxRecallDocument
	default:
		return MaxGenericDocument
	}
}

func knownSchema(name string) bool {
	for _, candidate := range schemaNames {
		if candidate == name {
			return true
		}
	}
	return false
}
