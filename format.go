package jqgo

import (
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"strings"
)

func applyFormat(name string, v any) (any, error) {
	switch name {
	case "text":
		return toString(v), nil
	case "json":
		return toJSON(v), nil
	case "html":
		return htmlEscaper.Replace(toString(v)), nil
	case "uri":
		return uriEscape(toString(v)), nil
	case "csv":
		return csvRow(v)
	case "tsv":
		return tsvRow(v)
	case "sh":
		return shQuote(v)
	case "base64":
		return base64.StdEncoding.EncodeToString([]byte(toString(v))), nil
	case "base64d":
		return base64Decode(toString(v))
	case "base32":
		return base32.StdEncoding.EncodeToString([]byte(toString(v))), nil
	case "base32d":
		s := strings.TrimRight(toString(v), "=")
		b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("%s is not valid base32 data", typeDump(v))
		}
		return validUTF8(b), nil
	}
	return nil, fmt.Errorf("%s is not a valid format", name)
}

var htmlEscaper = strings.NewReplacer("<", "&lt;", ">", "&gt;", "&", "&amp;", "'", "&apos;", "\"", "&quot;")

func uriEscape(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			sb.WriteByte(c)
		} else {
			fmt.Fprintf(&sb, "%%%02X", c)
		}
	}
	return sb.String()
}

func csvRow(v any) (any, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s cannot be csv-formatted, only an array can be", typeDump(v))
	}
	parts := make([]string, len(arr))
	for i, x := range arr {
		switch x := top(x).(type) {
		case nil:
		case bool, int, float64:
			parts[i] = toJSON(x)
		case string:
			parts[i] = `"` + strings.ReplaceAll(x, `"`, `""`) + `"`
		default:
			return nil, fmt.Errorf("%s is not valid in a csv row", typeDump(x))
		}
	}
	return strings.Join(parts, ","), nil
}

var tsvEscaper = strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\n", "\\n", "\r", "\\r")

func tsvRow(v any) (any, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s cannot be tsv-formatted, only an array can be", typeDump(v))
	}
	parts := make([]string, len(arr))
	for i, x := range arr {
		switch x := top(x).(type) {
		case nil:
		case bool, int, float64:
			parts[i] = toJSON(x)
		case string:
			parts[i] = tsvEscaper.Replace(x)
		default:
			return nil, fmt.Errorf("%s is not valid in a tsv row", typeDump(x))
		}
	}
	return strings.Join(parts, "\t"), nil
}

func shQuote(v any) (any, error) {
	quote := func(x any) (string, error) {
		switch x := top(x).(type) {
		case string:
			return "'" + strings.ReplaceAll(x, "'", `'\''`) + "'", nil
		case []any, map[string]any:
			return "", fmt.Errorf("%s can not be escaped for shell", typeDump(x))
		}
		return toJSON(x), nil
	}
	if arr, ok := v.([]any); ok {
		parts := make([]string, len(arr))
		for i, x := range arr {
			s, err := quote(x)
			if err != nil {
				return nil, err
			}
			parts[i] = s
		}
		return strings.Join(parts, " "), nil
	}
	s, err := quote(v)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func base64Decode(s string) (any, error) {
	trimmed := strings.TrimRight(s, "=")
	b, err := base64.RawStdEncoding.DecodeString(trimmed)
	if err != nil {
		// jq decodes whatever complete groups it can.
		if ce, ok := err.(base64.CorruptInputError); ok && int(ce) == len(trimmed)-1 && len(trimmed)%4 == 1 {
			b, err = base64.RawStdEncoding.DecodeString(trimmed[:len(trimmed)-1])
		}
		if err != nil {
			return nil, fmt.Errorf("%s is not valid base64 data", typeDump(s))
		}
	}
	return validUTF8(b), nil
}

func validUTF8(b []byte) string {
	return sanitizeUTF8(b)
}
