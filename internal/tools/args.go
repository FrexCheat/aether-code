package tools

import (
	"fmt"
	"strconv"
)

// argString fetches a required string argument: absent or empty is a failure.
func argString(args map[string]any, key string) (string, bool) {
	v, ok := args[key].(string)
	return v, ok && v != ""
}

// argInt fetches an optional integer argument. Absent is not an error; a
// present-but-non-integer value is.
func argInt(args map[string]any, key string) (int, error) {
	switch v := args[key].(type) {
	case nil:
		return 0, nil
	case float64:
		return int(v), nil
	case int:
		return v, nil
	case string:
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("argument %q must be an integer, got %q", key, v)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("argument %q must be an integer", key)
	}
}

// optionalStringArg fetches an optional string argument. An absent argument is
// an empty string; one that is present but not a string is an error.
func optionalStringArg(args map[string]any, key string) (string, error) {
	value, present := args[key]
	if !present {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", key)
	}
	return text, nil
}

func errResult(msg string) ToolResult {
	return ToolResult{Output: msg, IsError: true}
}
