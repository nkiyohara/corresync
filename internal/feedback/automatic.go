package feedback

import (
	"encoding/json"
	"errors"
)

const maximumAutomaticReportBytes = 8 << 10

// AutomaticInput contains the only values eligible for an explicitly enabled
// public GitHub issue. It deliberately excludes config, providers, account
// metadata, raw errors, argument values, paths, and environment values.
type AutomaticInput struct {
	Build         Build
	InstallMethod string
	LastError     ErrorRecord
}

// AutomaticReport is a smaller schema than the manually reviewed feedback
// report. Every dynamic string is validated against a closed allowlist before
// serialization.
type AutomaticReport struct {
	SchemaVersion int                       `json:"schema_version"`
	Privacy       AutomaticPrivacyStatement `json:"privacy"`
	Build         Build                     `json:"build"`
	Installation  SectionValue              `json:"installation"`
	LastError     LastErrorStatus           `json:"last_error"`
}

// AutomaticPrivacyStatement makes the public egress boundary machine-visible.
type AutomaticPrivacyStatement struct {
	Submission             string `json:"submission"`
	Destination            string `json:"destination"`
	RawErrorIncluded       bool   `json:"raw_error_included"`
	ArgumentValuesIncluded bool   `json:"argument_values_included"`
	AccountDataIncluded    bool   `json:"account_data_included"`
	ContentIncluded        bool   `json:"mail_or_calendar_content_included"`
	TaskContentIncluded    bool   `json:"task_content_included"`
}

// GenerateAutomatic builds the complete automatic issue payload from closed,
// bounded schemas. Unsafe input fails instead of being reflected or masked
// heuristically.
func GenerateAutomatic(input AutomaticInput) ([]byte, error) {
	if err := input.LastError.validate(); err != nil {
		return nil, errors.New("automatic feedback record is invalid")
	}
	report := AutomaticReport{
		SchemaVersion: reportVersion,
		Privacy: AutomaticPrivacyStatement{
			Submission:  "automatic-opt-in",
			Destination: "public-github-issue",
		},
		Build: sanitizeBuild(input.Build),
		Installation: sanitizeSectionValue(SectionValue{
			Status: "ok", Value: input.InstallMethod,
		}),
		LastError: sanitizeLastError(LastErrorStatus{
			Status:  "ok",
			ID:      input.LastError.ID,
			Command: &input.LastError.Command,
			Classes: input.LastError.Classes,
		}),
	}
	if report.LastError.Status != "ok" {
		return nil, errors.New("automatic feedback record did not pass its allowlist")
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maximumAutomaticReportBytes {
		return nil, errors.New("automatic feedback report exceeds its size limit")
	}
	return encoded, nil
}
