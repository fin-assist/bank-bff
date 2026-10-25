package app

import "testing"

// Every scenario runs at two levels: integration (the public API assembly on an
// httptest TCP server) and API (the complete Run with both listeners). Both send
// real HTTP and reach the peers over gRPC on bufconn; every non-SSE answer is
// validated against openapi.yaml and every SSE event against its x-sse-events schema.
var scenarios = map[string]func(*testing.T, starter){
	"StartPhone":            scenarioStartPhone,
	"RequestTooLarge":       scenarioRequestTooLarge,
	"WatchPhone":            scenarioWatchPhone,
	"RegisterRefreshLogout": scenarioRegisterRefreshLogout,
	"ConsentDocuments":      scenarioConsentDocuments,
	"Profile":               scenarioProfile,
	"KeyOutage":             scenarioKeyOutage,
	"AssistantUnary":        scenarioAssistantUnary,
	"WatchAnswer":           scenarioWatchAnswer,
	"Gates":                 scenarioGates,
	"Maintenance":           scenarioMaintenance,
	"StreamLimit":           scenarioStreamLimit,
	"ClientDisconnect":      scenarioClientDisconnect,
	"StreamDeadlines":       scenarioStreamOutlivesUnaryTimeouts,
}

func TestIntegrationScenarios(t *testing.T) {
	for name, run := range scenarios {
		t.Run(name, func(t *testing.T) { run(t, startAssembly) })
	}
}

func TestAPIScenarios(t *testing.T) {
	for name, run := range scenarios {
		t.Run(name, func(t *testing.T) { run(t, startBFF) })
	}
}
