// Package schemas validates the versioned artifact contracts embedded in the binary.
package schemas

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed *.schema.json
var definitions embed.FS

const (
	Source         = "source-content"
	Knowledge      = "knowledge"
	Manifest       = "artifact-manifest"
	TranscriptPart = "transcript-part"
	Visual         = "video-visual"
)

type Validator map[string]*jsonschema.Schema

func New() (Validator, error) {
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	validator := make(Validator)
	for _, name := range []string{Source, Visual, Knowledge, Manifest, TranscriptPart} {
		file := name + ".schema.json"
		data, err := definitions.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var definition any
		if err := json.Unmarshal(data, &definition); err != nil {
			return nil, err
		}
		if err := compiler.AddResource(file, definition); err != nil {
			return nil, err
		}
		validator[name], err = compiler.Compile(file)
		if err != nil {
			return nil, err
		}
	}
	return validator, nil
}

func (v Validator) Validate(name string, data []byte) error {
	schema, ok := v[name]
	if !ok {
		return fmt.Errorf("schema desconhecido: %s", name)
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if !json.Valid(data) || decoder.Decode(&value) != nil {
		return fmt.Errorf("JSON inválido: %s", name)
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("artefato fora do contrato: %s", name)
	}
	return nil
}
