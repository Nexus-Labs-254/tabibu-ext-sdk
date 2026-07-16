package sdk

import "github.com/tabibumrs/tabibu-ext-sdk/services"

// Type aliases for the patients service — the implementation lives in
// services/patients.go. Aliases (not new types) keep this fully transparent:
// sdk.Patient and services.Patient are the same type, so extension authors
// never need to import the services package themselves. Add one alias line
// here per exported symbol as new domain services are added.
type (
	PatientsService        = services.PatientsService
	Patient                = services.Patient
	PatientPerson          = services.PatientPerson
	RegisterPatientRequest = services.RegisterPatientRequest
	PatientsPage           = services.PatientsPage
)
