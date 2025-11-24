package logutil

import (
	"encoding/json"
	"log"
	"time"
)

// Info logs a structured info message.
func Info(msg string, fields map[string]interface{}) {
	logJSON("info", msg, fields)
}

// Error logs a structured error message including the error string.
func Error(msg string, err error, fields map[string]interface{}) {
	if fields == nil {
		fields = map[string]interface{}{}
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	logJSON("error", msg, fields)
}

func logJSON(level, msg string, fields map[string]interface{}) {
	if fields == nil {
		fields = map[string]interface{}{}
	}
	entry := map[string]interface{}{
		"level":     level,
		"message":   msg,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	}
	for k, v := range fields {
		entry[k] = v
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		log.Printf("%s: %+v", msg, fields)
		return
	}
	log.Printf("%s", payload)
}
