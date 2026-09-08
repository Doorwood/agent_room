package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

var (
	ErrDuplicateKey = errors.New("duplicate JSON object key")
	ErrTrailingJSON = errors.New("trailing JSON value")
	ErrUnknownField = errors.New("unknown JSON field")
)

var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

func ValidMessageID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for index := 0; index < len(id); index++ {
		byteValue := id[index]
		if !(byteValue >= '0' && byteValue <= '9') && !(byteValue >= 'a' && byteValue <= 'f') {
			return false
		}
	}
	return true
}

func decodeEnvelope(body []byte) (Envelope, error) {
	if err := validateJSON(body); err != nil {
		return Envelope{}, err
	}
	if err := validateExactCase(body, reflect.TypeFor[Envelope]()); err != nil {
		return Envelope{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, err
	}
	if err := ensureEOF(decoder); err != nil {
		return Envelope{}, err
	}
	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func DecodeBody[T any](body json.RawMessage) (T, error) {
	var value T
	if err := validateJSON(body); err != nil {
		return value, err
	}
	if err := validateExactCase(body, reflect.TypeFor[T]()); err != nil {
		return value, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if err := ensureEOF(decoder); err != nil {
		return value, err
	}
	if validator, ok := any(value).(interface{ Validate() error }); ok && !isNilJSONValue(value) {
		if err := validator.Validate(); err != nil {
			return value, err
		}
	}
	return value, nil
}

func validateExactCase(body []byte, expected reflect.Type) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := scanTypedJSONValue(decoder, expected); err != nil {
		return err
	}
	return ensureEOF(decoder)
}

func scanTypedJSONValue(decoder *json.Decoder, expected reflect.Type) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	return scanTypedJSONToken(decoder, token, expected)
}

func scanTypedJSONToken(decoder *json.Decoder, token json.Token, expected reflect.Type) error {
	if token == nil {
		return nil
	}
	for expected.Kind() == reflect.Pointer {
		expected = expected.Elem()
	}
	if expected == rawMessageType || expected.Kind() == reflect.Interface {
		return scanUntypedJSONToken(decoder, token)
	}

	delimiter, isDelimiter := token.(json.Delim)
	switch expected.Kind() {
	case reflect.Struct:
		if !isDelimiter || delimiter != '{' {
			return ErrInvalidEnvelope
		}
		fields := jsonFields(expected)
		seen := make(map[string]struct{}, len(fields))
		var unknownField error
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrInvalidEnvelope
			}
			field, exact := fields[key]
			if !exact {
				if alias, found := jsonFieldAlias(key, fields); found {
					if _, alreadySeen := seen[alias.name]; alreadySeen {
						return fmt.Errorf("%w: %q", ErrDuplicateKey, key)
					}
					seen[alias.name] = struct{}{}
					if err := scanTypedJSONValue(decoder, alias.typ); err != nil {
						return err
					}
					if unknownField == nil {
						unknownField = fmt.Errorf("%w: %q", ErrUnknownField, key)
					}
					continue
				}
				return fmt.Errorf("%w: %q", ErrUnknownField, key)
			}
			if _, alreadySeen := seen[field.name]; alreadySeen {
				return fmt.Errorf("%w: %q", ErrDuplicateKey, key)
			}
			seen[field.name] = struct{}{}
			if err := scanTypedJSONValue(decoder, field.typ); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return ErrInvalidEnvelope
		}
		if unknownField != nil {
			return unknownField
		}
		return nil
	case reflect.Slice, reflect.Array:
		if !isDelimiter || delimiter != '[' {
			return ErrInvalidEnvelope
		}
		for decoder.More() {
			if err := scanTypedJSONValue(decoder, expected.Elem()); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return ErrInvalidEnvelope
		}
		return nil
	case reflect.Map:
		if !isDelimiter || delimiter != '{' {
			return ErrInvalidEnvelope
		}
		for decoder.More() {
			if _, err := decoder.Token(); err != nil {
				return err
			}
			if err := scanTypedJSONValue(decoder, expected.Elem()); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return ErrInvalidEnvelope
		}
		return nil
	default:
		return scanUntypedJSONToken(decoder, token)
	}
}

func isNilJSONValue(value any) bool {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() {
		return true
	}
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

type jsonField struct {
	name string
	typ  reflect.Type
}

func jsonFields(expected reflect.Type) map[string]jsonField {
	fields := make(map[string]jsonField, expected.NumField())
	for index := 0; index < expected.NumField(); index++ {
		field := expected.Field(index)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = jsonField{name: name, typ: field.Type}
	}
	return fields
}

func jsonFieldAlias(key string, fields map[string]jsonField) (jsonField, bool) {
	for name, field := range fields {
		if strings.EqualFold(key, name) {
			return field, true
		}
	}
	return jsonField{}, false
}

func scanUntypedJSONToken(decoder *json.Decoder, token json.Token) error {
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		for decoder.More() {
			if _, err := decoder.Token(); err != nil {
				return err
			}
			if err := scanUntypedJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return ErrInvalidEnvelope
		}
	case '[':
		for decoder.More() {
			if err := scanUntypedJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return ErrInvalidEnvelope
		}
	default:
		return ErrInvalidEnvelope
	}
	return nil
}

func scanUntypedJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	return scanUntypedJSONToken(decoder, token)
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return ErrTrailingJSON
		}
		return err
	}
	return nil
}

func validateJSON(body []byte) error {
	if !utf8.Valid(body) {
		return ErrInvalidUTF8
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	return ensureEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("%w: object key", ErrInvalidEnvelope)
			}
			if _, exists := keys[key]; exists {
				return fmt.Errorf("%w: %q", ErrDuplicateKey, key)
			}
			keys[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("%w: object end", ErrInvalidEnvelope)
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("%w: array end", ErrInvalidEnvelope)
		}
	default:
		return fmt.Errorf("%w: unexpected delimiter", ErrInvalidEnvelope)
	}
	return nil
}
