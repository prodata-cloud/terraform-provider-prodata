package client

import (
	"encoding/json"
	"net/http"
	"strings"
)

// v1Response is the legacy ("V1") wire envelope returned by panel-main's
// load-balancer endpoints on success: {"error":0,"errMessage":...,"data":<T>}.
// It is distinct from the V2 envelope ({"success",...}) decoded by parseResponse.
type v1Response struct {
	Error      int             `json:"error"`
	ErrMessage string          `json:"errMessage"`
	Data       json.RawMessage `json:"data"`
}

// parseV1Response interprets a response from a V1 (load-balancer) endpoint and
// decodes its data payload into T.
//
// panel-main's V1 LB endpoints are not uniform on the wire:
//   - success            -> V1 envelope {"error":0,"errMessage":...,"data":<T>}
//   - ApiException        -> V2 envelope {"success":false,"errors":[{code,message}]} + HTTP 4xx/5xx
//   - legacy failures     -> V1 envelope with a non-zero "error" field
//   - infra failures      -> non-JSON body (e.g. an nginx error page) + HTTP 5xx
//
// It returns (data, nil) on success or (zero, *APIError) on any failure. A V2
// *success* body is rejected rather than silently accepted: that shape only
// reaches here when a caller targeted the wrong endpoint, and treating it as a
// V1 success would mask the mistake.
func parseV1Response[T any](statusCode int, body []byte) (T, *APIError) {
	var zero T

	var fields map[string]json.RawMessage
	jsonOK := json.Unmarshal(body, &fields) == nil
	_, hasV1Error := fields["error"]
	_, hasV2Success := fields["success"]

	switch {
	case jsonOK && hasV1Error:
		var env v1Response
		if err := json.Unmarshal(body, &env); err != nil {
			return zero, &APIError{
				StatusCode: statusCode,
				Message:    "parse V1 response: " + err.Error(),
				RawBody:    string(body),
			}
		}
		if env.Error != 0 {
			return zero, &APIError{
				StatusCode: statusCode,
				Codes:      []int{env.Error},
				Message:    env.ErrMessage,
				RawBody:    string(body),
			}
		}
		var data T
		if len(env.Data) > 0 && string(env.Data) != "null" {
			if err := json.Unmarshal(env.Data, &data); err != nil {
				return zero, &APIError{
					StatusCode: statusCode,
					Message:    "parse V1 data: " + err.Error(),
					RawBody:    string(body),
				}
			}
		}
		return data, nil

	case jsonOK && hasV2Success:
		// A V2 envelope reaching a V1 endpoint is only ever an error (an
		// ApiException rendered by ApiExceptionHandler). A V2 success body here
		// means the client hit the wrong endpoint — surface it, never accept it.
		var v2 apiResponse[json.RawMessage]
		_ = json.Unmarshal(body, &v2)
		apiErr := &APIError{StatusCode: statusCode, RawBody: string(body)}
		if len(v2.Errors) > 0 {
			msgs := make([]string, len(v2.Errors))
			for i, e := range v2.Errors {
				apiErr.Codes = append(apiErr.Codes, e.Code)
				msgs[i] = e.Message
			}
			apiErr.Message = strings.Join(msgs, "; ")
		} else {
			apiErr.Message = "unexpected V2-shape response from a V1 endpoint"
		}
		return zero, apiErr

	default:
		// Non-JSON or unrecognized body — an infra error page, empty body, etc.
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = http.StatusText(statusCode)
		}
		return zero, &APIError{StatusCode: statusCode, Message: msg, RawBody: string(body)}
	}
}
