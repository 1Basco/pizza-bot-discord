package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	pizzaLogKey      string
	pizzaServiceName = "pizza-bot"
	pizzaHTTPClient  = &http.Client{Timeout: 5 * time.Second}
)

func initLogger() {
	pizzaLogKey = os.Getenv("PIZZA_LOG")
	if sn := os.Getenv("SERVICE_NAME"); sn != "" {
		pizzaServiceName = sn
	}
}

var severityMap = map[string]int{
	"TRACE": 1, "DEBUG": 5, "INFO": 9,
	"WARN": 13, "ERROR": 17, "FATAL": 21,
}

// OTLP JSON structures

type kvPair struct {
	Key   string      `json:"key"`
	Value kvPairValue `json:"value"`
}

type kvPairValue struct {
	StringValue string `json:"stringValue"`
}

type logRecordBody struct {
	StringValue string `json:"stringValue"`
}

type logRecord struct {
	TimeUnixNano   string        `json:"timeUnixNano"`
	SeverityNumber int           `json:"severityNumber"`
	SeverityText   string        `json:"severityText"`
	Body           logRecordBody `json:"body"`
	Attributes     []kvPair      `json:"attributes,omitempty"`
	TraceId        string        `json:"traceId,omitempty"`
	SpanId         string        `json:"spanId,omitempty"`
	ParentSpanId   string        `json:"parentSpanId,omitempty"`
}

type scopeLog struct {
	LogRecords []logRecord `json:"logRecords"`
}

type resourceAttrs struct {
	Attributes []kvPair `json:"attributes"`
}

type resourceLog struct {
	Resource  resourceAttrs `json:"resource"`
	ScopeLogs []scopeLog    `json:"scopeLogs"`
}

type otlpPayload struct {
	ResourceLogs []resourceLog `json:"resourceLogs"`
}

// Log sends a fire-and-forget log to PizzaLog. No-op if PIZZA_LOG is not set.
func Log(level, message string, attrs map[string]string) {
	LogTrace(level, message, attrs, "", "", "")
}

// LogTrace is like Log but links the record into a distributed trace.
func LogTrace(level, message string, attrs map[string]string, traceId, spanId, parentSpanId string) {
	if pizzaLogKey == "" {
		return
	}
	upper := strings.ToUpper(level)
	sevNum, ok := severityMap[upper]
	if !ok {
		sevNum = 9
		upper = "INFO"
	}

	rec := logRecord{
		TimeUnixNano:   strconv.FormatInt(time.Now().UnixNano(), 10),
		SeverityNumber: sevNum,
		SeverityText:   upper,
		Body:           logRecordBody{StringValue: message},
		TraceId:        traceId,
		SpanId:         spanId,
		ParentSpanId:   parentSpanId,
	}

	for k, v := range attrs {
		rec.Attributes = append(rec.Attributes, kvPair{
			Key:   k,
			Value: kvPairValue{StringValue: v},
		})
	}

	go sendLog(rec)
}

func sendLog(rec logRecord) {
	payload := otlpPayload{
		ResourceLogs: []resourceLog{{
			Resource: resourceAttrs{
				Attributes: []kvPair{{
					Key:   "service.name",
					Value: kvPairValue{StringValue: pizzaServiceName},
				}},
			},
			ScopeLogs: []scopeLog{{
				LogRecords: []logRecord{rec},
			}},
		}},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return
	}

	req, err := http.NewRequest("POST", "https://logs.pizzaria.foundation/v1/logs", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+pizzaLogKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := pizzaHTTPClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// NewTraceID returns a cryptographically random 32-char hex string.
func NewTraceID() string { return randHex(16) }

// NewSpanID returns a cryptographically random 16-char hex string.
func NewSpanID() string { return randHex(8) }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
