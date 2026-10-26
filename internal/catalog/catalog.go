// Package catalog is the single controlled catalog of user-facing error texts and
// severities of bank-bff (docs/api.md, «Единое отображение ошибок», FIN-224/FIN-225).
// Internal services return codes and typed parameters; only this catalog turns a code
// into text. Texts are fixed templates: no service message, user input, SQL, provider
// or model output is ever inserted.
package catalog

// Severity values of user_message (an open set in the contract).
const (
	Info    = "info"
	Warning = "warning"
	Error   = "error"
)

// Entry is the presentation of one code.
type Entry struct {
	// Text is a safe plain-text message, 1–1000 code points.
	Text string
	// Severity is info, warning or error.
	Severity string
	// Diagnostic is the English error.message for logs; never user text.
	Diagnostic string
}

// rest holds REST error codes (docs/api.md, section 8; FIN-339 REQUEST_TOO_LARGE).
var rest = map[string]Entry{
	"VALIDATION_ERROR":           {"Проверьте введённые данные.", Warning, "Request validation failed"},
	"CLIENT_VERSION_UNSUPPORTED": {"Эта версия приложения больше не поддерживается. Обновите приложение.", Warning, "Client version is not supported"},
	"INVALID_TOKEN":              {"Сессия истекла. Войдите снова.", Warning, "Token is invalid or expired"},
	"TOKEN_EXPIRED":              {"Время на подтверждение вышло. Начните вход заново.", Warning, "Token expired"},
	"TOKEN_REVOKED":              {"Сессия завершена. Войдите снова.", Warning, "Token revoked"},
	"CONSENT_REQUIRED":           {"Чтобы продолжить, нужно согласие.", Info, "Consent required"},
	"LIMIT_EXCEEDED":             {"Вопросы на сегодня закончились. Новые появятся в 00:00 по Москве.", Info, "Daily question limit exceeded"},
	"NOT_FOUND":                  {"Не нашли то, что вы ищете.", Warning, "Not found"},
	"IDEMPOTENCY_CONFLICT":       {"Этот запрос уже отправлялся с другими данными. Повторите действие.", Error, "Idempotency-Key was used with another request"},
	"REQUEST_IN_PROGRESS":        {"Запрос ещё выполняется. Подождите немного.", Info, "Request with this Idempotency-Key is in progress"},
	"FILE_TOO_LARGE":             {"Файл больше 10 МБ.", Warning, "File too large"},
	"WRONG_FORMAT":               {"Нужна выписка в формате OFX.", Warning, "Not an OFX file"},
	"CSV_NOT_ACCEPTED":           {"CSV не подходит — выгрузите выписку в формате OFX.", Warning, "CSV is not accepted"},
	"WRONG_BANK":                 {"Выберите OFX-выписку Т-Банка.", Warning, "OFX is not from T-Bank"},
	"CONSENT_OUTDATED":           {"Документ согласия обновился. Ознакомьтесь с новой версией.", Info, "Consent document version is outdated"},
	"TOO_MANY_RESULTS":           {"Слишком много операций. Сузьте период.", Warning, "Too many results"},
	"SELECTION_NOT_FOUND":        {"Эта подборка больше недоступна.", Warning, "Selection not found"},
	"CATEGORY_NOT_ASSIGNABLE":    {"Эту категорию нельзя назначить.", Warning, "Category is not assignable"},
	"RETRY_NOT_ALLOWED":          {"Этот ответ повторить нельзя.", Warning, "Retry is not allowed"},
	"RATE_LIMITED":               {"Слишком много попыток. Попробуйте позже.", Warning, "Too many attempts"},
	"INTERNAL_ERROR":             {"Что-то пошло не так. Попробуйте позже.", Error, "Internal error"},
	"MAINTENANCE":                {"Идут технические работы. Попробуйте позже.", Warning, "Maintenance in progress"},
	"REQUEST_TOO_LARGE":          {"Запрос слишком большой.", Error, "Request body too large"},
}

// unknownREST is the safe text of a code absent from the catalog.
var unknownREST = Entry{"Сейчас выполнить действие не получилось. Попробуйте позже.", Error, "Request failed"}

// REST returns the presentation of a REST error code and whether the code is known.
// An unknown code gets the safe generic entry.
func REST(code string) (Entry, bool) {
	e, ok := rest[code]
	if !ok {
		return unknownREST, false
	}
	return e, true
}

// Answer error codes of the assistant terminal event and failed history (docs/api.md 7.2).
var answer = map[string]Entry{
	"MODEL_ERROR":   {"Не получилось ответить. Вопрос не потрачен", Error, "Answer failed"},
	"CANNOT_ANSWER": {"Не получилось ответить — недостаточно данных, переформулируйте вопрос. Вопрос не потрачен", Warning, "Answer failed"},
	"OFF_TOPIC":     {"Вопрос не по теме — помощник отвечает только на вопросы о ваших финансах. С другими вопросами помогут универсальные ассистенты: Алиса AI, GigaChat, ChatGPT, Claude, DeepSeek", Warning, "Answer failed"},
}

// unknownAnswer is the scenario fallback text of the assistant (INTERNAL_ERROR and any
// code without a catalog entry).
var unknownAnswer = Entry{"Не получилось ответить.", Error, "Answer failed"}

// Answer returns the presentation of an assistant result code and whether it is known.
func Answer(code string) (Entry, bool) {
	e, ok := answer[code]
	if !ok {
		return unknownAnswer, false
	}
	return e, true
}

// RESTCodes lists every REST code of the catalog, for tests and metrics initialisation.
func RESTCodes() []string {
	codes := make([]string, 0, len(rest))
	for c := range rest {
		codes = append(codes, c)
	}
	return codes
}

// AnswerCodes lists every assistant result code of the catalog.
func AnswerCodes() []string {
	codes := make([]string, 0, len(answer))
	for c := range answer {
		codes = append(codes, c)
	}
	return codes
}

// Import returns safe presentation of a terminal import error, independent of
// original-request retry permission. Unknown result reasons remain open.
func Import(code string) (Entry, bool) {
	switch code {
	case "PROCESSING_FAILED":
		return Entry{"Не получилось обработать выписку. Попробуйте загрузить её снова.", Error, "Import failed"}, true
	case "CANCELLED":
		return Entry{"Загрузка отменена.", Info, "Import cancelled"}, true
	default:
		return Entry{"Не получилось обработать выписку. Попробуйте загрузить её снова.", Error, "Import failed"}, false
	}
}
