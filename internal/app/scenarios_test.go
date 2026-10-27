package app

import "testing"

// Scenarios in this legacy shared table run at two levels: integration (the public API assembly on an
// httptest TCP server) and API (the complete Run with both listeners). Both send
// real HTTP and reach the peers over gRPC on bufconn; every non-SSE answer is
// validated against openapi.yaml and every SSE event against its x-sse-events schema.
// New FIN-368/FIN-339 source and encoding flows are API-only helpers without a
// starter parameter; their component integrations are separate from this table.
var scenarios = map[string]func(*testing.T, starter){
	"OperationMutationTransportLoss": scenarioOperationMutationTransportLoss,
	"OperationOtherMalformed":        scenarioOperationOtherMalformed,
	"Operations":                     scenarioOperations, "OperationMutations": scenarioOperationMutations, "OperationsErrorsAndGates": scenarioOperationsErrorsAndGates, "OperationLargeSearch": scenarioOperationLargeSearch, "OperationMalformed": scenarioOperationMalformed,
	"AnalyticsLocalYearZero": scenarioAnalyticsLocalYearZero, "AnalyticsEnvelope": scenarioAnalyticsEnvelope, "AnalyticsMalformed": scenarioAnalyticsMalformed, "Analytics": scenarioAnalytics, "AnalyticsPeriods": scenarioAnalyticsPeriods, "AnalyticsErrors": scenarioAnalyticsErrors,
	"ImportLifetime": scenarioImportLifetime, "ImportMultipartEdges": scenarioImportMultipartEdges,
	"ImportUnknownReason": scenarioImportUnknownReason,
	"ImportCapacity":      scenarioImportCapacity, "ImportEarlyLength": scenarioImportEarlyLength,
	"ImportStreamingBeforeEOF": scenarioImportStreamingBeforeEOF,
	"ImportShutdown":           scenarioImportShutdown,
	"ImportRead":               scenarioImportRead, "ImportUpload": scenarioImportUpload, "ImportWatch": scenarioImportWatch, "ImportErrors": scenarioImportErrors,
	"StartPhone":            scenarioStartPhone,
	"RequestTooLarge":       scenarioRequestTooLarge,
	"WatchPhone":            scenarioWatchPhone,
	"RegisterRefreshLogout": scenarioRegisterRefreshLogout,
	"ConsentDocuments":      scenarioConsentDocuments,
	"Profile":               scenarioProfile,
	"ProfileMutations":      scenarioProfileMutations,
	"ProfileDeletion":       scenarioProfileDeletion,
	"KeyOutage":             scenarioKeyOutage,
	"AssistantUnary":        scenarioAssistantUnary,
	"WatchAnswer":           scenarioWatchAnswer,
	"Gates":                 scenarioGates,
	"Maintenance":           scenarioMaintenance,
	"StreamLimit":           scenarioStreamLimit,
	"ClientDisconnect":      scenarioClientDisconnect,
	"StreamDeadlines":       scenarioStreamOutlivesUnaryTimeouts,
	"ShutdownEndsStreams":   scenarioShutdownEndsStreams,
	"OperationErrors":       scenarioOperationErrors,
	"RequestRetryAdvice":    scenarioRequestRetryAdvice,
	"ResultStatus":          scenarioResultStatus,
	"TrustedProxies":        scenarioTrustedProxies,
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
