package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

func yamlJSON(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d := yaml.NewDecoder(f)
	var node yaml.Node
	if err := d.Decode(&node); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: expected one YAML mapping", path)
	}
	if err := checkYAML(&node); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var value any
	if err := node.Decode(&value); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%s: expected exactly one YAML document", path)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return data, nil
}

func checkYAML(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode {
		return fmt.Errorf("line %d: YAML aliases are not supported", n.Line)
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Tag != "!!str" {
				return fmt.Errorf("line %d: mapping keys must be strings", k.Line)
			}
			if seen[k.Value] {
				return fmt.Errorf("line %d: duplicate key %q", k.Line, k.Value)
			}
			seen[k.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := checkYAML(child); err != nil {
			return err
		}
	}
	return nil
}

func decode(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	return checkFields(raw, reflect.ValueOf(target), "")
}

func checkFields(raw any, v reflect.Value, path string) error {
	if raw == nil {
		switch v.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
			return nil
		default:
			return fmt.Errorf("%s must not be null", path)
		}
	}
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	switch x := raw.(type) {
	case map[string]any:
		if v.Kind() == reflect.Map && v.Type().Key().Kind() == reflect.String {
			for key, child := range x {
				if err := checkFields(child, v.MapIndex(reflect.ValueOf(key)), path+"."+key); err != nil {
					return err
				}
			}
			return nil
		}
		if v.Kind() != reflect.Struct {
			return nil
		} // JSON Schema and headers are open maps.
		fields := map[string]reflect.Value{}
		union := false
		for i := 0; i < v.NumField(); i++ {
			tag := strings.Split(v.Type().Field(i).Tag.Get("json"), ",")[0]
			if tag == "-" {
				union = true
			}
			if tag == "-" && v.Field(i).Kind() == reflect.Pointer && !v.Field(i).IsNil() {
				return checkFields(raw, v.Field(i), path)
			}
			if tag != "" && tag != "-" {
				fields[tag] = v.Field(i)
			}
		}
		if union {
			return fmt.Errorf("%s.type must select a union variant", path)
		}
		for k, child := range x {
			fv, ok := fields[k]
			if !ok {
				return fmt.Errorf("unknown field %s.%s", path, k)
			}
			if err := checkFields(child, fv, path+"."+k); err != nil {
				return err
			}
		}
	case []any:
		if v.Kind() == reflect.Slice {
			for i, child := range x {
				if err := checkFields(child, v.Index(i), fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
