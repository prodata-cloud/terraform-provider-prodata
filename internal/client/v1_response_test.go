package client

import (
	"encoding/json"
	"testing"
)

// V1 success envelope — data decodes into T.
func TestParseV1Response_Success(t *testing.T) {
	body := []byte(`{"error":0,"errMessage":null,"data":{"id":42,"name":"lb-1","isPublic":true,"status":{"name":"SUCCESS"},"source":"FRONTEND"}}`)
	dto, apiErr := parseV1Response[*lbDTO](200, body)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if dto == nil {
		t.Fatal("expected non-nil dto")
	}
	if dto.ID != 42 || dto.Name != "lb-1" {
		t.Errorf("dto = %+v, want id 42 name lb-1", dto)
	}
	if dto.Status.Name != "SUCCESS" {
		t.Errorf("status.name = %q, want SUCCESS", dto.Status.Name)
	}
}

// Round-2 finding 5.1 — a V1 envelope with data:null decodes to (nil, nil),
// not an error. Locks in nullable-data behavior.
func TestParseV1Response_NullData(t *testing.T) {
	body := []byte(`{"error":0,"errMessage":"","data":null}`)
	dto, apiErr := parseV1Response[*lbDTO](200, body)
	if apiErr != nil {
		t.Fatalf("expected no error for null data, got: %v", apiErr)
	}
	if dto != nil {
		t.Errorf("expected nil dto for null data, got %+v", dto)
	}
}

// A V1 envelope with a non-zero "error" field is a failure.
func TestParseV1Response_V1ErrorField(t *testing.T) {
	body := []byte(`{"error":736,"errMessage":"Load balancer not found.","data":null}`)
	dto, apiErr := parseV1Response[*lbDTO](200, body)
	if apiErr == nil {
		t.Fatal("expected APIError")
	}
	if !apiErr.HasCode(736) {
		t.Errorf("expected code 736, got %v", apiErr.Codes)
	}
	if dto != nil {
		t.Errorf("expected nil dto on error, got %+v", dto)
	}
}

// Round-2 finding 5.2 — a V2-shape success body must be rejected, never
// silently accepted as a V1 success. V1/V2 confusion is otherwise undetectable.
func TestParseV1Response_V2SuccessRejected(t *testing.T) {
	body := []byte(`{"success":true,"data":{"id":42,"name":"lb-1"}}`)
	dto, apiErr := parseV1Response[*lbDTO](200, body)
	if apiErr == nil {
		t.Fatal("expected V2-shape success body to be rejected")
	}
	if dto != nil {
		t.Errorf("expected nil dto, got %+v", dto)
	}
}

// An ApiException on a V1 endpoint is rendered as the V2 error envelope; the
// adapter must surface its codes and HTTP status.
func TestParseV1Response_V2ErrorEnvelope(t *testing.T) {
	body := []byte(`{"success":false,"data":null,"errors":[{"code":736,"message":"Load balancer not found."}]}`)
	_, apiErr := parseV1Response[*lbDTO](404, body)
	if apiErr == nil {
		t.Fatal("expected APIError")
	}
	if apiErr.StatusCode != 404 {
		t.Errorf("StatusCode = %d, want 404", apiErr.StatusCode)
	}
	if !apiErr.HasCode(736) {
		t.Errorf("expected code 736, got %v", apiErr.Codes)
	}
	if !IsNotFound(apiErr) {
		t.Error("IsNotFound should be true for a 736 error")
	}
}

// Round-2 finding 5.3 — a cross-source operation (a Frontend endpoint hit on a
// CCM-source LB) returns {"error":500,...}; the adapter must surface code 500,
// never mask it as 736.
func TestParseV1Response_CrossSourceError(t *testing.T) {
	body := []byte(`{"error":500,"errMessage":"Cannot do this operation with this resource","data":null}`)
	_, apiErr := parseV1Response[json.RawMessage](200, body)
	if apiErr == nil {
		t.Fatal("expected APIError")
	}
	if !apiErr.HasCode(500) {
		t.Errorf("expected code 500, got %v", apiErr.Codes)
	}
	if apiErr.HasCode(736) {
		t.Error("cross-source 500 must NOT be masked as 736")
	}
	if IsNotFound(apiErr) {
		t.Error("cross-source 500 must NOT be treated as not-found")
	}
}

// A non-JSON body (infra error page) is reported with its HTTP status.
func TestParseV1Response_MalformedBody(t *testing.T) {
	_, apiErr := parseV1Response[*lbDTO](502, []byte("Bad Gateway"))
	if apiErr == nil {
		t.Fatal("expected APIError for non-JSON body")
	}
	if apiErr.StatusCode != 502 {
		t.Errorf("StatusCode = %d, want 502", apiErr.StatusCode)
	}
}

// A V1 envelope whose data is a JSON array decodes into a slice.
func TestParseV1Response_ListData(t *testing.T) {
	body := []byte(`{"error":0,"errMessage":null,"data":[{"id":1,"name":"a"},{"id":2,"name":"b"}]}`)
	dtos, apiErr := parseV1Response[[]lbDTO](200, body)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if len(dtos) != 2 {
		t.Fatalf("len = %d, want 2", len(dtos))
	}
	if dtos[0].ID != 1 || dtos[1].ID != 2 {
		t.Errorf("ids = %d,%d, want 1,2", dtos[0].ID, dtos[1].ID)
	}
}
