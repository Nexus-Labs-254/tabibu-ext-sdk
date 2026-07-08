package sdk

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Nexus-Labs-254/tabibu-ext-sdk/internal"
)

// PatientsService provides read and write access to the patients domain.
// Calls are routed through the stdio IPC channel to the Extension Runtime,
// which forwards them to the server's patients module.
type PatientsService interface {
	// List returns patients whose name or ID matches query. Pass an empty
	// string to return all patients.
	List(ctx context.Context, query string) ([]Patient, error)

	// Get returns a single patient by UUID.
	Get(ctx context.Context, id string) (Patient, error)

	// Register creates a new patient record.
	Register(ctx context.Context, req RegisterPatientRequest) (Patient, error)
}

// Patient is a patient record as returned by the Extension Runtime.
// Fields mirror the server's patients.models.Patient JSON output.
type Patient struct {
	ID            string        `json:"id"`
	BloodGroup    *string       `json:"blood_group,omitempty"`
	AllergyStatus string        `json:"allergy_status"`
	CreatedAt     string        `json:"created_at"`
	Person        PatientPerson `json:"person"`
}

// PatientPerson holds the demographic and contact data for a patient.
// Fields mirror the server's persons.models.Person JSON output.
type PatientPerson struct {
	ID                 string  `json:"id"`
	GivenName          string  `json:"given_name,omitempty"`
	MiddleName         string  `json:"middle_name,omitempty"`
	FamilyName         string  `json:"family_name,omitempty"`
	Salutation         string  `json:"salutation,omitempty"`
	Sex                string  `json:"sex"`
	Birthdate          *string `json:"birthdate,omitempty"`
	BirthdateEstimated bool    `json:"birthdate_estimated"`
	PrimaryPhone       *string `json:"primary_phone,omitempty"`
	AltPhone           *string `json:"alt_phone,omitempty"`
	Email              *string `json:"email,omitempty"`
	PhotoURL           *string `json:"photo_url,omitempty"`
}

// RegisterPatientRequest is the payload for registering a new patient.
// Fields mirror the server's patients.models.RegisterRequest.
type RegisterPatientRequest struct {
	GivenName          string  `json:"given_name"`
	MiddleName         string  `json:"middle_name,omitempty"`
	FamilyName         string  `json:"family_name"`
	Salutation         string  `json:"salutation,omitempty"`
	Sex                string  `json:"sex"`
	Birthdate          string  `json:"birthdate,omitempty"`
	BirthdateEstimated bool    `json:"birthdate_estimated,omitempty"`
	BloodGroup         *string `json:"blood_group,omitempty"`
	AllergyStatus      string  `json:"allergy_status,omitempty"`
	Phone              string  `json:"primary_phone,omitempty"`
	AltPhone           string  `json:"alt_phone,omitempty"`
	Email              string  `json:"email,omitempty"`
}

// patientsService is the concrete implementation backed by the IPC conn.
type patientsService struct {
	conn *internal.Conn
}

var _ PatientsService = (*patientsService)(nil)

func (s *patientsService) List(ctx context.Context, query string) ([]Patient, error) {
	payload, _ := json.Marshal(map[string]string{"query": query})
	res, err := s.call(ctx, "list", payload)
	if err != nil {
		return nil, err
	}
	var patients []Patient
	if err := json.Unmarshal(res, &patients); err != nil {
		return nil, fmt.Errorf("patients.list: decode response: %w", err)
	}
	return patients, nil
}

func (s *patientsService) Get(ctx context.Context, id string) (Patient, error) {
	payload, _ := json.Marshal(map[string]string{"id": id})
	res, err := s.call(ctx, "get", payload)
	if err != nil {
		return Patient{}, err
	}
	var p Patient
	if err := json.Unmarshal(res, &p); err != nil {
		return Patient{}, fmt.Errorf("patients.get: decode response: %w", err)
	}
	return p, nil
}

func (s *patientsService) Register(ctx context.Context, req RegisterPatientRequest) (Patient, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return Patient{}, fmt.Errorf("patients.register: marshal request: %w", err)
	}
	res, err := s.call(ctx, "register", payload)
	if err != nil {
		return Patient{}, err
	}
	var p Patient
	if err := json.Unmarshal(res, &p); err != nil {
		return Patient{}, fmt.Errorf("patients.register: decode response: %w", err)
	}
	return p, nil
}

// call sends a service_req to the Extension Runtime and returns the data
// from the service_res, or an error if the runtime reports a failure.
func (s *patientsService) call(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
	reqPayload, _ := json.Marshal(internal.ServiceReqPayload{
		Service: "patients",
		Method:  method,
		Payload: payload,
	})
	msg := internal.Message{
		Type: internal.MsgServiceReq,
		Data: json.RawMessage(reqPayload),
	}
	resp, err := s.conn.Call(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("patients.%s: ipc: %w", method, err)
	}
	var res internal.ServiceResPayload
	if err := json.Unmarshal(resp.Data, &res); err != nil {
		return nil, fmt.Errorf("patients.%s: decode service_res: %w", method, err)
	}
	if !res.OK {
		return nil, fmt.Errorf("patients.%s: %s", method, res.Error)
	}
	return res.Data, nil
}
