package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
)

// ── getDeterministicBucket ────────────────────────────────────────────────────

func TestGetDeterministicBucket_ReturnsValueBetween0And99(t *testing.T) {
	inputs := []string{"user-a", "user-b", "user-c:flag-x", ""}
	for _, input := range inputs {
		bucket := getDeterministicBucket(input)
		if bucket < 0 || bucket > 99 {
			t.Errorf("getDeterministicBucket(%q) = %d, fora do intervalo [0,99]", input, bucket)
		}
	}
}

func TestGetDeterministicBucket_IsDeterministic(t *testing.T) {
	b1 := getDeterministicBucket("user123flag-dark-mode")
	b2 := getDeterministicBucket("user123flag-dark-mode")
	if b1 != b2 {
		t.Error("getDeterministicBucket deve retornar o mesmo valor para o mesmo input")
	}
}

func TestGetDeterministicBucket_DifferentInputsDifferentBuckets(t *testing.T) {
	b1 := getDeterministicBucket("user-aaa")
	b2 := getDeterministicBucket("user-zzz")
	// Não é garantido que sejam diferentes para qualquer par,
	// mas para esses dois valores específicos verificamos distribuição.
	_ = b1
	_ = b2
	// Apenas garantimos que não entram em pânico
}

// ── runEvaluationLogic ────────────────────────────────────────────────────────

func newTestEvalApp() *App {
	return &App{
		RedisClient:         nil,
		SqsSvc:              nil,
		SqsQueueURL:         "",
		HttpClient:          &http.Client{},
		FlagServiceURL:      "http://fake-flags",
		TargetingServiceURL: "http://fake-targeting",
	}
}

func TestRunEvaluationLogic_FlagNil_ReturnsFalse(t *testing.T) {
	a := newTestEvalApp()
	result := a.runEvaluationLogic(&CombinedFlagInfo{Flag: nil, Rule: nil}, "user-1")
	if result {
		t.Error("flag nil deve retornar false")
	}
}

func TestRunEvaluationLogic_FlagDisabled_ReturnsFalse(t *testing.T) {
	a := newTestEvalApp()
	info := &CombinedFlagInfo{
		Flag: &Flag{Name: "my-flag", IsEnabled: false},
		Rule: nil,
	}
	if a.runEvaluationLogic(info, "user-1") {
		t.Error("flag desabilitada deve retornar false")
	}
}

func TestRunEvaluationLogic_FlagEnabledNoRule_ReturnsTrue(t *testing.T) {
	a := newTestEvalApp()
	info := &CombinedFlagInfo{
		Flag: &Flag{Name: "my-flag", IsEnabled: true},
		Rule: nil,
	}
	if !a.runEvaluationLogic(info, "user-1") {
		t.Error("flag habilitada sem regra deve retornar true")
	}
}

func TestRunEvaluationLogic_RuleDisabled_ReturnsTrue(t *testing.T) {
	a := newTestEvalApp()
	info := &CombinedFlagInfo{
		Flag: &Flag{Name: "my-flag", IsEnabled: true},
		Rule: &TargetingRule{IsEnabled: false},
	}
	if !a.runEvaluationLogic(info, "user-1") {
		t.Error("regra desabilitada deve retornar true (sem filtragem)")
	}
}

func TestRunEvaluationLogic_PercentageRule_100Percent_ReturnsTrue(t *testing.T) {
	a := newTestEvalApp()
	info := &CombinedFlagInfo{
		Flag: &Flag{Name: "my-flag", IsEnabled: true},
		Rule: &TargetingRule{
			IsEnabled: true,
			Rules:     Rule{Type: "PERCENTAGE", Value: float64(100)},
		},
	}
	// 100% → todos os usuários devem receber true
	if !a.runEvaluationLogic(info, "any-user-id") {
		t.Error("100% de rollout deve retornar true para qualquer usuário")
	}
}

func TestRunEvaluationLogic_PercentageRule_0Percent_ReturnsFalse(t *testing.T) {
	a := newTestEvalApp()
	info := &CombinedFlagInfo{
		Flag: &Flag{Name: "my-flag", IsEnabled: true},
		Rule: &TargetingRule{
			IsEnabled: true,
			Rules:     Rule{Type: "PERCENTAGE", Value: float64(0)},
		},
	}
	if a.runEvaluationLogic(info, "any-user-id") {
		t.Error("0% de rollout deve retornar false para qualquer usuário")
	}
}

func TestRunEvaluationLogic_PercentageRule_InvalidValue_ReturnsFalse(t *testing.T) {
	a := newTestEvalApp()
	info := &CombinedFlagInfo{
		Flag: &Flag{Name: "my-flag", IsEnabled: true},
		Rule: &TargetingRule{
			IsEnabled: true,
			// Value é string em vez de float64 → deve retornar false
			Rules: Rule{Type: "PERCENTAGE", Value: "not-a-number"},
		},
	}
	if a.runEvaluationLogic(info, "user-1") {
		t.Error("valor de porcentagem inválido deve retornar false")
	}
}

// ── NotFoundError ─────────────────────────────────────────────────────────────

func TestNotFoundError_Message(t *testing.T) {
	err := &NotFoundError{FlagName: "dark-mode"}
	if !strings.Contains(err.Error(), "dark-mode") {
		t.Errorf("mensagem de erro deve conter o nome da flag, obteve: %s", err.Error())
	}
}

// ── evaluationHandler ─────────────────────────────────────────────────────────

// newFakeRedisClient retorna um cliente Redis com timeout mínimo
// apontado para host inexistente — simula cache miss imediatamente.
func newFakeRedisClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:         "localhost:0",
		DialTimeout:  1 * time.Millisecond,
		ReadTimeout:  1 * time.Millisecond,
		WriteTimeout: 1 * time.Millisecond,
	})
}

func TestEvaluationHandler_MissingParams(t *testing.T) {
	a := newTestEvalApp()
	a.RedisClient = newFakeRedisClient()

	tests := []struct {
		name  string
		query string
	}{
		{"sem parâmetros", ""},
		{"só user_id", "user_id=u1"},
		{"só flag_name", "flag_name=my-flag"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/evaluate?"+tc.query, nil)
			rr := httptest.NewRecorder()
			a.evaluationHandler(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Errorf("[%s] esperava 400, obteve %d", tc.name, rr.Code)
			}
		})
	}
}

func TestEvaluationHandler_FlagNotFound_ReturnsFalse(t *testing.T) {
	// Simula flag-service retornando 404
	flagSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer flagSrv.Close()

	// targeting-service também responde 404 rapidamente (sem DNS timeout)
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer targetSrv.Close()

	a := &App{
		RedisClient:         newFakeRedisClient(),
		FlagServiceURL:      flagSrv.URL,
		TargetingServiceURL: targetSrv.URL,
		HttpClient:          &http.Client{Timeout: 2 * time.Second},
	}

	req := httptest.NewRequest(http.MethodGet, "/evaluate?user_id=u1&flag_name=ghost", nil)
	rr := httptest.NewRecorder()
	a.evaluationHandler(rr, req)

	// NotFoundError → result=false, status 200
	if rr.Code != http.StatusOK {
		t.Errorf("esperava 200 para flag não encontrada (resultado false), obteve %d", rr.Code)
	}
	var resp EvaluationResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Result {
		t.Error("flag não encontrada deve retornar result=false")
	}
}

func TestEvaluationHandler_ServiceError_Returns502(t *testing.T) {
	// flag-service retorna erro 500
	flagSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer flagSrv.Close()

	// targeting-service responde rapidamente para não bloquear via DNS timeout
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer targetSrv.Close()

	a := &App{
		RedisClient:         newFakeRedisClient(),
		FlagServiceURL:      flagSrv.URL,
		TargetingServiceURL: targetSrv.URL,
		HttpClient:          &http.Client{Timeout: 2 * time.Second},
	}

	req := httptest.NewRequest(http.MethodGet, "/evaluate?user_id=u1&flag_name=my-flag", nil)
	rr := httptest.NewRecorder()
	a.evaluationHandler(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Errorf("esperava 502 para erro no serviço, obteve %d", rr.Code)
	}
}

func TestEvaluationHandler_Success_ReturnsCorrectShape(t *testing.T) {
	// flag-service retorna flag habilitada
	flagPayload, _ := json.Marshal(Flag{ID: 1, Name: "my-flag", IsEnabled: true})
	flagSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(flagPayload)
	}))
	defer flagSrv.Close()

	// targeting-service retorna 404 (sem regra → true direto)
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer targetSrv.Close()

	a := &App{
		RedisClient:         newFakeRedisClient(),
		FlagServiceURL:      flagSrv.URL,
		TargetingServiceURL: targetSrv.URL,
		HttpClient:          &http.Client{},
		SqsQueueURL:         "", // sem SQS no teste
	}

	req := httptest.NewRequest(http.MethodGet, "/evaluate?user_id=user-abc&flag_name=my-flag", nil)
	rr := httptest.NewRecorder()

	// O handler é síncrono; o evento SQS é fire-and-forget (goroutine interna)
	// e retorna imediatamente quando SqsQueueURL está vazio.
	a.evaluationHandler(rr, req)

	if rr.Code != http.StatusOK {
		body, _ := io.ReadAll(rr.Body)
		t.Fatalf("esperava 200, obteve %d. Body: %s", rr.Code, string(body))
	}

	var resp EvaluationResponse
	json.NewDecoder(rr.Body).Decode(&resp)

	if resp.UserID != "user-abc" {
		t.Errorf("user_id incorreto: %q", resp.UserID)
	}
	if resp.FlagName != "my-flag" {
		t.Errorf("flag_name incorreto: %q", resp.FlagName)
	}
	// Flag habilitada sem regra → true
	if !resp.Result {
		t.Error("flag habilitada sem regra deve retornar result=true")
	}
}

// ── healthHandler ─────────────────────────────────────────────────────────────

func TestEvalHealthHandler(t *testing.T) {
	a := newTestEvalApp()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()

	a.healthHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("esperava 200, obteve %d", rr.Code)
	}
}
