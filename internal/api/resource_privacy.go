package api

import (
	"encoding/json"
)

// publicResourceProjection removes protected fields at the HTTP boundary while
// preserving the store's complete typed records for authenticated and admin
// reads. Raw JSON values keep large integer and decimal encodings intact.
type (
	publicResourceProjection[T any] struct {
		value  T
		remove []string
		// extra holds fields the store type does not carry, added at the boundary.
		extra map[string]any
	}
)

func (p publicResourceProjection[T]) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(p.value)
	if err != nil {
		return nil, err
	}

	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for _, field := range p.remove {
		delete(fields, field)
	}
	for key, value := range p.extra {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		fields[key] = encoded
	}
	return json.Marshal(fields)
}

func publicResource[T any](value T, remove ...string) publicResourceProjection[T] {
	return publicResourceProjection[T]{value: value, remove: remove}
}

var (
	publicContactFields     = []string{"email", "phone", "website"}
	publicWantContactFields = []string{
		"contactPermission",
		"contactText",
		"email",
		"phone",
	}
)
