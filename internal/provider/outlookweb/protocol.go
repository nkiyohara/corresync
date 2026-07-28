package outlookweb

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	exchange2013 = "Exchange2013"
	defaultZone  = "UTC"
)

var responseCodePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,127}$`)

type requestHeader struct {
	Type                 string          `json:"__type"`
	RequestServerVersion string          `json:"RequestServerVersion"`
	TimeZoneContext      timeZoneContext `json:"TimeZoneContext"`
}

type timeZoneContext struct {
	Type               string             `json:"__type"`
	TimeZoneDefinition timeZoneDefinition `json:"TimeZoneDefinition"`
}

type timeZoneDefinition struct {
	Type string `json:"__type"`
	ID   string `json:"Id"`
}

type responseMessages[T any] struct {
	Items []T `json:"Items"`
}

type responseEnvelope[T any] struct {
	Body T `json:"Body"`
}

// ProtocolError contains only a sanitized machine response code.
type ProtocolError struct {
	Action       string
	ResponseCode string
}

func (failure *ProtocolError) Error() string {
	return fmt.Sprintf("OWA %s failed with %s", failure.Action, failure.ResponseCode)
}

func newRequestHeader(zone string) requestHeader {
	if zone == "" {
		zone = defaultZone
	}
	return requestHeader{
		Type:                 "JsonRequestHeaders:#Exchange",
		RequestServerVersion: exchange2013,
		TimeZoneContext: timeZoneContext{
			Type: "TimeZoneContext:#Exchange",
			TimeZoneDefinition: timeZoneDefinition{
				Type: "TimeZoneDefinitionType:#Exchange",
				ID:   zone,
			},
		},
	}
}

func checkResponse(action, class, code string) error {
	if class == "Success" && code == "NoError" {
		return nil
	}
	if !responseCodePattern.MatchString(code) {
		code = "UnknownResponse"
	}
	return &ProtocolError{Action: action, ResponseCode: code}
}

// checkWriteResponse distinguishes an explicit single-item OWA failure from
// a malformed or partial response whose remote side effect is uncertain.
func checkWriteResponse(action Action, class, code string) error {
	failure := checkResponse(action.Name(), class, code)
	if failure == nil {
		return nil
	}
	if class == "Error" && strings.HasPrefix(code, "Error") && responseCodePattern.MatchString(code) {
		return failure
	}
	return classifyPostRequestError(action, failure)
}

func validateZone(zone string) error {
	if zone == "" {
		return nil
	}
	if len(zone) > 128 || strings.TrimSpace(zone) != zone || strings.ContainsAny(zone, "\r\n\x00") {
		return errors.New("invalid OWA time zone")
	}
	return nil
}
