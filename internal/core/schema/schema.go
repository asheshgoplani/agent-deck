// Package schema derives JSON Schema documents from Go types by reflection.
// It is the only source of command schemas: nothing here is hand-written per
// command, and golden tests pin the output so a struct change is visible in
// review.
package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Draft is the JSON Schema dialect emitted.
const Draft = "https://json-schema.org/draft/2020-12/schema"

// Schema is a JSON Schema node. Only the keywords the reflector emits exist.
type Schema struct {
	Draft                string             `json:"$schema,omitempty"`
	ID                   string             `json:"$id,omitempty"`
	Ref                  string             `json:"$ref,omitempty"`
	Title                string             `json:"title,omitempty"`
	Description          string             `json:"description,omitempty"`
	Type                 string             `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties any                `json:"additionalProperties,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Defs                 map[string]*Schema `json:"$defs,omitempty"`
}

var timeType = reflect.TypeOf(time.Time{})

// Reflect returns the schema of t. Named struct types other than the root
// are emitted once under $defs and referenced, so recursive types terminate.
func Reflect(t reflect.Type, id, title string) (*Schema, error) {
	r := &reflector{defs: map[string]*Schema{}, names: map[reflect.Type]string{}}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	root, err := r.structOrType(t, true)
	if err != nil {
		return nil, err
	}
	root.Draft = Draft
	root.ID = id
	root.Title = title
	if len(r.defs) > 0 {
		root.Defs = r.defs
	}
	return root, nil
}

// Marshal renders a schema as indented JSON with a trailing newline.
func Marshal(s *Schema) ([]byte, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

type reflector struct {
	defs  map[string]*Schema
	names map[reflect.Type]string
}

func (r *reflector) structOrType(t reflect.Type, root bool) (*Schema, error) {
	if t.Kind() == reflect.Struct && t != timeType && !root && t.Name() != "" {
		return r.ref(t)
	}
	return r.typeSchema(t)
}

// ref emits t under $defs (once) and returns a reference to it.
func (r *reflector) ref(t reflect.Type) (*Schema, error) {
	name, seen := r.names[t]
	if !seen {
		name = r.defName(t)
		r.names[t] = name
		r.defs[name] = &Schema{} // placeholder breaks recursion
		s, err := r.typeSchema(t)
		if err != nil {
			return nil, err
		}
		r.defs[name] = s
	}
	return &Schema{Ref: "#/$defs/" + name}, nil
}

// defName is the package-qualified type name ("session.ListStats"); a clash
// between two packages of the same base name falls back to the full path.
func (r *reflector) defName(t reflect.Type) string {
	pkg := t.PkgPath()
	base := pkg[strings.LastIndex(pkg, "/")+1:]
	name := base + "." + t.Name()
	for other, used := range r.names {
		if used == name && other != t {
			return strings.ReplaceAll(pkg, "/", ".") + "." + t.Name()
		}
	}
	return name
}

func (r *reflector) typeSchema(t reflect.Type) (*Schema, error) {
	if t == timeType {
		return &Schema{Type: "string", Format: "date-time"}, nil
	}
	switch t.Kind() {
	case reflect.Pointer:
		return r.structOrType(t.Elem(), false)
	case reflect.Bool:
		return &Schema{Type: "boolean"}, nil
	case reflect.String:
		return &Schema{Type: "string"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return &Schema{Type: "number"}, nil
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return &Schema{Type: "string", Format: "byte"}, nil
		}
		items, err := r.structOrType(t.Elem(), false)
		if err != nil {
			return nil, err
		}
		return &Schema{Type: "array", Items: items}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("schema: map key %s is not a string", t.Key())
		}
		vals, err := r.structOrType(t.Elem(), false)
		if err != nil {
			return nil, err
		}
		return &Schema{Type: "object", AdditionalProperties: vals}, nil
	case reflect.Interface:
		return &Schema{}, nil
	case reflect.Struct:
		return r.objectSchema(t)
	}
	return nil, fmt.Errorf("schema: unsupported kind %s (%s)", t.Kind(), t)
}

func (r *reflector) objectSchema(t reflect.Type) (*Schema, error) {
	s := &Schema{Type: "object", Properties: map[string]*Schema{}, AdditionalProperties: false}
	if err := r.addFields(s, t); err != nil {
		return nil, err
	}
	sort.Strings(s.Required)
	return s, nil
}

func (r *reflector) addFields(s *Schema, t reflect.Type) error {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, omitempty, skip := jsonName(f)
		if skip {
			continue
		}
		if f.Anonymous && f.Tag.Get("json") == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				if err := r.addFields(s, ft); err != nil {
					return err
				}
				continue
			}
		}
		fs, err := r.structOrType(f.Type, false)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", t.Name(), f.Name, err)
		}
		if doc := f.Tag.Get("doc"); doc != "" {
			if fs.Ref != "" {
				// $ref siblings are allowed in 2020-12; keep the ref intact.
				fs = &Schema{Ref: fs.Ref, Description: doc}
			} else {
				fs.Description = doc
			}
		}
		s.Properties[name] = fs
		if !omitempty && f.Type.Kind() != reflect.Pointer {
			s.Required = append(s.Required, name)
		}
	}
	return nil
}

func jsonName(f reflect.StructField) (name string, omitempty, skip bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		name = f.Name
	}
	for _, opt := range parts[1:] {
		if opt == "omitempty" || opt == "omitzero" {
			omitempty = true
		}
	}
	return name, omitempty, false
}
