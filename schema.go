package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaJSON is claude-skills' process/schema.json, the state contract shared by every process phase and proc.
// Copy it over unchanged when it changes there.
//
//go:embed schema/schema.json
var schemaJSON []byte

const schemaURL = "https://github.com/JohnnyMcGee/claude-skills/process/schema.json"

var schemas = map[string]*jsonschema.Schema{}

func init() {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		panic(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaURL, doc); err != nil {
		panic(err)
	}
	for _, kind := range []string{"queue_entry", "decision", "result"} {
		schemas[kind] = c.MustCompile(schemaURL + "#/$defs/" + kind)
	}
}

// validate checks any JSON-encodable value against one of the contract's definitions.
func validate(kind string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	if err := schemas[kind].Validate(doc); err != nil {
		return fmt.Errorf("%s: %w", kind, err)
	}
	return nil
}
