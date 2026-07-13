// Package normalization canonically encodes values after request-specific
// semantic normalization has already been applied.
package normalization

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"rhizome-mcp/internal/domain"
)

const (
	defaultMaxInputBytes    = 1 << 20
	defaultMaxDepth         = 64
	defaultMaxObjectMembers = 4096
	defaultMaxArrayElements = 4096
	defaultMaxOutputBytes   = 1 << 20
)

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[interface{ MarshalText() ([]byte, error) }]()
	jsonNumberType    = reflect.TypeFor[json.Number]()
	jsonNumberPattern = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)
)

// Limits bounds work performed by a Canonicalizer. Each bound is inclusive;
// MaxInputBytes applies to raw JSON while all other bounds apply to both APIs.
// Use DefaultLimits and override individual fields for application policy.
type Limits struct {
	MaxInputBytes    int
	MaxDepth         int
	MaxObjectMembers int
	MaxArrayElements int
	MaxOutputBytes   int
}

// DefaultLimits returns conservative limits suitable for idempotency requests.
func DefaultLimits() Limits {
	return Limits{
		MaxInputBytes:    defaultMaxInputBytes,
		MaxDepth:         defaultMaxDepth,
		MaxObjectMembers: defaultMaxObjectMembers,
		MaxArrayElements: defaultMaxArrayElements,
		MaxOutputBytes:   defaultMaxOutputBytes,
	}
}

// Canonicalizer deterministically encodes already semantically normalized data.
// It is immutable after construction and safe for concurrent use.
type Canonicalizer struct {
	limits Limits
}

// NewCanonicalizer constructs a Canonicalizer. Negative limits are invalid;
// zero is a valid bound.
func NewCanonicalizer(limits Limits) (*Canonicalizer, error) {
	if limits.MaxInputBytes < 0 || limits.MaxDepth < 0 || limits.MaxObjectMembers < 0 || limits.MaxArrayElements < 0 || limits.MaxOutputBytes < 0 {
		return nil, invalidError("normalization limits are invalid", "INVALID_LIMIT")
	}
	return &Canonicalizer{limits: limits}, nil
}

// EncodeNormalized canonically encodes a value after caller-owned semantic
// normalization (for example trimming, case folding, and defaulting).
//
// Supported values are nil, booleans, strings, json.Number, finite floats,
// signed and unsigned integers, pointers, interfaces, arrays, non-byte slices,
// maps with string keys, and structs composed from those values. Structs use
// encoding/json field, tag, ",string", and omission semantics, so omitted data
// does not affect the result. All reachable struct fields are validated even if
// encoding/json omits them. Custom JSON/text marshalers and unsupported kinds
// are rejected rather than allowed to define incidental encodings. Numbers
// retain exact decimal value, discard insignificant spelling differences, use
// plain notation for adjusted exponents [-6, 21), and scientific notation
// otherwise; conversion through float64 never occurs.
func (c *Canonicalizer) EncodeNormalized(value any) ([]byte, error) {
	if c == nil {
		return nil, invalidError("normalization canonicalizer is required", "MISSING_CANONICALIZER")
	}
	if err := validateGoValue(reflect.ValueOf(value), 0, c.limits, make(map[visit]struct{})); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, invalidError("normalized value cannot be represented as JSON", "INVALID_JSON_VALUE")
	}
	return c.encodeRaw(raw, false)
}

// EncodeNormalizedJSON canonically encodes raw JSON whose semantic
// normalization has already occurred. Generic JSON has no schema, so all
// object keys are meaningful; duplicate keys and trailing values are rejected.
func (c *Canonicalizer) EncodeNormalizedJSON(raw []byte) ([]byte, error) {
	if c == nil {
		return nil, invalidError("normalization canonicalizer is required", "MISSING_CANONICALIZER")
	}
	return c.encodeRaw(raw, true)
}

// HashNormalized returns the SHA-256 digest of EncodeNormalized.
func (c *Canonicalizer) HashNormalized(value any) ([sha256.Size]byte, error) {
	encoded, err := c.EncodeNormalized(value)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

// HashNormalizedJSON returns the SHA-256 digest of EncodeNormalizedJSON.
func (c *Canonicalizer) HashNormalizedJSON(raw []byte) ([sha256.Size]byte, error) {
	encoded, err := c.EncodeNormalizedJSON(raw)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

type visit struct {
	typ reflect.Type
	ptr uintptr
}

func validateGoValue(value reflect.Value, depth int, limits Limits, active map[visit]struct{}) error {
	if !value.IsValid() {
		return nil
	}
	typ := value.Type()
	if typ == jsonNumberType {
		if value.Len() > limits.MaxOutputBytes {
			return limitError("canonical JSON output exceeds its byte limit", "MAX_OUTPUT_BYTES", limits.MaxOutputBytes)
		}
		if _, err := canonicalNumber(value.String()); err != nil {
			return invalidError("normalized value contains an invalid JSON number", "INVALID_NUMBER")
		}
		return nil
	}
	if typ.Implements(jsonMarshalerType) || typ.Implements(textMarshalerType) || (reflect.PointerTo(typ).Implements(jsonMarshalerType) || reflect.PointerTo(typ).Implements(textMarshalerType)) {
		return invalidError("normalized value contains a custom marshaler", "UNSUPPORTED_VALUE")
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return nil
		}
		return validateGoValue(value.Elem(), depth, limits, active)
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		key := visit{typ: typ, ptr: value.Pointer()}
		if _, exists := active[key]; exists {
			return invalidError("normalized value contains a cycle", "UNSUPPORTED_VALUE")
		}
		active[key] = struct{}{}
		defer delete(active, key)
		return validateGoValue(value.Elem(), depth, limits, active)
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return nil
	case reflect.Float32, reflect.Float64:
		if math.IsNaN(value.Float()) || math.IsInf(value.Float(), 0) {
			return invalidError("normalized value contains a non-finite number", "INVALID_NUMBER")
		}
		return nil
	case reflect.String:
		if value.Len() > limits.MaxOutputBytes {
			return limitError("canonical JSON output exceeds its byte limit", "MAX_OUTPUT_BYTES", limits.MaxOutputBytes)
		}
		return validateString(value.String())
	case reflect.Array, reflect.Slice:
		if value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8 {
			return invalidError("normalized value contains an unsupported byte slice", "UNSUPPORTED_VALUE")
		}
		if value.Kind() == reflect.Slice && value.IsNil() {
			return nil
		}
		if depth >= limits.MaxDepth {
			return limitError("normalized JSON exceeds its nesting-depth limit", "MAX_DEPTH", limits.MaxDepth)
		}
		if value.Len() > limits.MaxArrayElements {
			return limitError("normalized JSON array exceeds its element limit", "MAX_ARRAY_ELEMENTS", limits.MaxArrayElements)
		}
		for index := 0; index < value.Len(); index++ {
			if err := validateGoValue(value.Index(index), depth+1, limits, active); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return invalidError("normalized value contains a map with non-string keys", "UNSUPPORTED_VALUE")
		}
		if value.IsNil() {
			return nil
		}
		if depth >= limits.MaxDepth {
			return limitError("normalized JSON exceeds its nesting-depth limit", "MAX_DEPTH", limits.MaxDepth)
		}
		if value.Len() > limits.MaxObjectMembers {
			return limitError("normalized JSON object exceeds its member limit", "MAX_OBJECT_MEMBERS", limits.MaxObjectMembers)
		}
		key := visit{typ: typ, ptr: value.Pointer()}
		if _, exists := active[key]; exists {
			return invalidError("normalized value contains a cycle", "UNSUPPORTED_VALUE")
		}
		active[key] = struct{}{}
		defer delete(active, key)
		keys := value.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		for _, mapKey := range keys {
			if err := validateString(mapKey.String()); err != nil {
				return err
			}
			if err := validateGoValue(value.MapIndex(mapKey), depth+1, limits, active); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		if depth >= limits.MaxDepth {
			return limitError("normalized JSON exceeds its nesting-depth limit", "MAX_DEPTH", limits.MaxDepth)
		}
		for index := 0; index < value.NumField(); index++ {
			if err := validateGoValue(value.Field(index), depth+1, limits, active); err != nil {
				return err
			}
		}
		return nil
	default:
		return invalidError("normalized value contains an unsupported Go value", "UNSUPPORTED_VALUE")
	}
}

func validateString(value string) error {
	if !utf8.ValidString(value) {
		return invalidError("normalized JSON contains invalid UTF-8", "INVALID_UTF8")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return invalidError("normalized JSON contains a NUL character", "NUL_NOT_ALLOWED")
	}
	return nil
}

type jsonValue struct {
	kind    byte
	text    string
	boolean bool
	array   []jsonValue
	object  []jsonMember
}

type jsonMember struct {
	key   string
	value jsonValue
}

func (c *Canonicalizer) encodeRaw(raw []byte, enforceInputBound bool) ([]byte, error) {
	if enforceInputBound && len(raw) > c.limits.MaxInputBytes {
		return nil, limitError("normalized JSON input exceeds its byte limit", "MAX_INPUT_BYTES", c.limits.MaxInputBytes)
	}
	if !utf8.Valid(raw) {
		return nil, invalidError("normalized JSON contains invalid UTF-8", "INVALID_UTF8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := c.decodeValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, invalidError("normalized JSON contains trailing or malformed data", "TRAILING_DATA")
	}

	writer := boundedWriter{maximum: c.limits.MaxOutputBytes}
	if err := encodeCanonical(&writer, value); err != nil {
		return nil, err
	}
	return writer.bytes(), nil
}

func (c *Canonicalizer) decodeValue(decoder *json.Decoder, depth int) (jsonValue, error) {
	token, err := decoder.Token()
	if err != nil {
		return jsonValue{}, invalidError("normalized JSON is malformed", "MALFORMED_JSON")
	}
	switch token := token.(type) {
	case nil:
		return jsonValue{kind: 'n'}, nil
	case bool:
		return jsonValue{kind: 'b', boolean: token}, nil
	case string:
		if err := validateString(token); err != nil {
			return jsonValue{}, err
		}
		return jsonValue{kind: 's', text: token}, nil
	case json.Number:
		number, err := canonicalNumber(token.String())
		if err != nil {
			return jsonValue{}, invalidError("normalized JSON contains an invalid number", "INVALID_NUMBER")
		}
		return jsonValue{kind: '#', text: number}, nil
	case json.Delim:
		if depth >= c.limits.MaxDepth {
			return jsonValue{}, limitError("normalized JSON exceeds its nesting-depth limit", "MAX_DEPTH", c.limits.MaxDepth)
		}
		switch token {
		case '{':
			return c.decodeObject(decoder, depth+1)
		case '[':
			return c.decodeArray(decoder, depth+1)
		default:
			return jsonValue{}, invalidError("normalized JSON is malformed", "MALFORMED_JSON")
		}
	default:
		return jsonValue{}, invalidError("normalized JSON contains an unsupported value", "UNSUPPORTED_VALUE")
	}
}

func (c *Canonicalizer) decodeObject(decoder *json.Decoder, depth int) (jsonValue, error) {
	members := make([]jsonMember, 0)
	seen := make(map[string]struct{})
	for decoder.More() {
		if len(members) >= c.limits.MaxObjectMembers {
			return jsonValue{}, limitError("normalized JSON object exceeds its member limit", "MAX_OBJECT_MEMBERS", c.limits.MaxObjectMembers)
		}
		keyToken, err := decoder.Token()
		if err != nil {
			return jsonValue{}, invalidError("normalized JSON is malformed", "MALFORMED_JSON")
		}
		key, ok := keyToken.(string)
		if !ok {
			return jsonValue{}, invalidError("normalized JSON object key is invalid", "MALFORMED_JSON")
		}
		if err := validateString(key); err != nil {
			return jsonValue{}, err
		}
		if _, exists := seen[key]; exists {
			return jsonValue{}, invalidError("normalized JSON contains a duplicate object key", "DUPLICATE_KEY")
		}
		seen[key] = struct{}{}
		value, err := c.decodeValue(decoder, depth)
		if err != nil {
			return jsonValue{}, err
		}
		members = append(members, jsonMember{key: key, value: value})
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return jsonValue{}, invalidError("normalized JSON is malformed", "MALFORMED_JSON")
	}
	sort.Slice(members, func(i, j int) bool { return members[i].key < members[j].key })
	return jsonValue{kind: 'o', object: members}, nil
}

func (c *Canonicalizer) decodeArray(decoder *json.Decoder, depth int) (jsonValue, error) {
	values := make([]jsonValue, 0)
	for decoder.More() {
		if len(values) >= c.limits.MaxArrayElements {
			return jsonValue{}, limitError("normalized JSON array exceeds its element limit", "MAX_ARRAY_ELEMENTS", c.limits.MaxArrayElements)
		}
		value, err := c.decodeValue(decoder, depth)
		if err != nil {
			return jsonValue{}, err
		}
		values = append(values, value)
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
		return jsonValue{}, invalidError("normalized JSON is malformed", "MALFORMED_JSON")
	}
	return jsonValue{kind: 'a', array: values}, nil
}

func canonicalNumber(raw string) (string, error) {
	if !jsonNumberPattern.MatchString(raw) {
		return "", errors.New("invalid JSON number")
	}
	negative := strings.HasPrefix(raw, "-")
	unsigned := raw
	if negative {
		unsigned = raw[1:]
	}
	exponentText := "0"
	if index := strings.IndexAny(unsigned, "eE"); index >= 0 {
		exponentText = unsigned[index+1:]
		unsigned = unsigned[:index]
	}
	fractionDigits := 0
	if index := strings.IndexByte(unsigned, '.'); index >= 0 {
		fractionDigits = len(unsigned) - index - 1
		unsigned = unsigned[:index] + unsigned[index+1:]
	}
	if unsigned == "" || !allDigits(unsigned) || exponentText == "" {
		return "", errors.New("invalid JSON number")
	}
	exponent := new(big.Int)
	if _, ok := exponent.SetString(exponentText, 10); !ok {
		return "", errors.New("invalid JSON exponent")
	}
	digits := strings.TrimLeft(unsigned, "0")
	if digits == "" {
		return "0", nil
	}
	trailingZeros := len(digits) - len(strings.TrimRight(digits, "0"))
	digits = strings.TrimRight(digits, "0")
	exponent.Sub(exponent, big.NewInt(int64(fractionDigits)))
	exponent.Add(exponent, big.NewInt(int64(trailingZeros)))
	scientificExponent := new(big.Int).Add(new(big.Int).Set(exponent), big.NewInt(int64(len(digits)-1)))

	var result string
	if scientificExponent.Cmp(big.NewInt(-6)) >= 0 && scientificExponent.Cmp(big.NewInt(21)) < 0 {
		position := new(big.Int).Add(exponent, big.NewInt(int64(len(digits))))
		point := int(position.Int64())
		switch {
		case point <= 0:
			result = "0." + strings.Repeat("0", -point) + digits
		case point >= len(digits):
			result = digits + strings.Repeat("0", point-len(digits))
		default:
			result = digits[:point] + "." + digits[point:]
		}
	} else {
		result = digits[:1]
		if len(digits) > 1 {
			result += "." + digits[1:]
		}
		result += "e" + scientificExponent.String()
	}
	if negative {
		result = "-" + result
	}
	return result, nil
}

func allDigits(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

type boundedWriter struct {
	buffer  bytes.Buffer
	maximum int
}

func (w *boundedWriter) writeString(value string) error {
	if len(value) > w.maximum-w.buffer.Len() {
		return limitError("canonical JSON output exceeds its byte limit", "MAX_OUTPUT_BYTES", w.maximum)
	}
	_, _ = w.buffer.WriteString(value)
	return nil
}

func (w *boundedWriter) bytes() []byte {
	return append([]byte(nil), w.buffer.Bytes()...)
}

func encodeCanonical(writer *boundedWriter, value jsonValue) error {
	switch value.kind {
	case 'n':
		return writer.writeString("null")
	case 'b':
		if value.boolean {
			return writer.writeString("true")
		}
		return writer.writeString("false")
	case 's':
		return encodeString(writer, value.text)
	case '#':
		return writer.writeString(value.text)
	case 'a':
		if err := writer.writeString("["); err != nil {
			return err
		}
		for index, element := range value.array {
			if index > 0 {
				if err := writer.writeString(","); err != nil {
					return err
				}
			}
			if err := encodeCanonical(writer, element); err != nil {
				return err
			}
		}
		return writer.writeString("]")
	case 'o':
		if err := writer.writeString("{"); err != nil {
			return err
		}
		for index, member := range value.object {
			if index > 0 {
				if err := writer.writeString(","); err != nil {
					return err
				}
			}
			if err := encodeString(writer, member.key); err != nil {
				return err
			}
			if err := writer.writeString(":"); err != nil {
				return err
			}
			if err := encodeCanonical(writer, member.value); err != nil {
				return err
			}
		}
		return writer.writeString("}")
	default:
		return invalidError("normalized JSON contains an unsupported value", "UNSUPPORTED_VALUE")
	}
}

func encodeString(writer *boundedWriter, value string) error {
	if err := writer.writeString("\""); err != nil {
		return err
	}
	start := 0
	for index, character := range value {
		escaped := ""
		switch character {
		case '\\':
			escaped = `\\`
		case '"':
			escaped = `\"`
		case '\b':
			escaped = `\b`
		case '\f':
			escaped = `\f`
		case '\n':
			escaped = `\n`
		case '\r':
			escaped = `\r`
		case '\t':
			escaped = `\t`
		default:
			if character < 0x20 {
				escaped = fmt.Sprintf(`\u%04x`, character)
			}
		}
		if escaped == "" {
			continue
		}
		if err := writer.writeString(value[start:index]); err != nil {
			return err
		}
		if err := writer.writeString(escaped); err != nil {
			return err
		}
		start = index + utf8.RuneLen(character)
	}
	if err := writer.writeString(value[start:]); err != nil {
		return err
	}
	return writer.writeString("\"")
}

func invalidError(message, detailCode string) *domain.Error {
	return domain.NewError(
		domain.CodeInvalidArgument,
		message,
		false,
		domain.Detail{Field: "normalized_json", Code: detailCode},
	)
}

func limitError(message, detailCode string, maximum int) *domain.Error {
	return domain.NewError(
		domain.CodeLimitExceeded,
		message,
		false,
		domain.Detail{Field: "normalized_json", Code: detailCode, Message: fmt.Sprintf("maximum %d", maximum)},
	)
}
