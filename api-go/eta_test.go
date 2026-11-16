package main

// delivery-eta Go 서비스 계약 테스트.
//
// HTTP 계약은 api/tests/test_app.py 와 같은 입력·기대값으로 확인한다. 실제 서버나 네트워크 없이
// httptest 로 핸들러를 직접 호출하고, 예측기는 Python rule 구현체와 같은 공식
// (10 + 3*거리 + 2*대기주문)의 가짜를 넣는다. 그래서 ONNX Runtime 없이도 돈다.
//
// TestONNXPredictor 만 실제 모델을 쓴다. ONNXRUNTIME_LIB 와 MODEL_PATH 가 가리키는 파일이
// 없으면 건너뛴다.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

const testModel = "test-rule"

// rulePredictor 는 Python api/predictors/rule.py 와 같은 공식이다.
type rulePredictor struct{}

func (rulePredictor) Predict(d, p float32) (float64, error) {
	return 10 + 3*float64(d) + 2*float64(p), nil
}

// fixedPredictor 는 정해진 값이나 오류를 돌려준다 (500 경로 확인용).
type fixedPredictor struct {
	minutes float64
	err     error
}

func (f fixedPredictor) Predict(float32, float32) (float64, error) { return f.minutes, f.err }

type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) json(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatalf("응답이 JSON 객체가 아니다: %v (%s)", err, r.body)
	}
	return v
}

func (r response) minutes(t *testing.T) float64 {
	t.Helper()
	m, ok := r.json(t)["minutes"].(float64)
	if !ok {
		t.Fatalf("minutes 가 숫자가 아니다: %s", r.body)
	}
	return m
}

// locs 는 422 응답 detail 의 loc 목록을 "body.field" 형태 문자열로 돌려준다.
func (r response) locs(t *testing.T) []string {
	t.Helper()
	detail, ok := r.json(t)["detail"].([]any)
	if !ok || len(detail) == 0 {
		t.Fatalf("detail 이 비어 있지 않은 목록이 아니다: %s", r.body)
	}
	var out []string
	for _, d := range detail {
		var parts []string
		for _, p := range d.(map[string]any)["loc"].([]any) {
			parts = append(parts, fmt.Sprint(p))
		}
		out = append(out, strings.Join(parts, "."))
	}
	return out
}

func do(t *testing.T, p Predictor, method, path, body string, contentType string) response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	newMux(p, testModel).ServeHTTP(rec, req)
	return response{status: rec.Code, header: rec.Header(), body: rec.Body.Bytes()}
}

func postETA(t *testing.T, body string) response {
	t.Helper()
	return do(t, rulePredictor{}, http.MethodPost, "/eta", body, "application/json")
}

func eta(d, p string) string {
	return fmt.Sprintf(`{"distance_km": %s, "pending_orders": %s}`, d, p)
}

func approx(got, want, rel float64) bool {
	return math.Abs(got-want) <= rel*math.Max(1, math.Abs(want))
}

func assertStatus(t *testing.T, r response, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("상태 코드 %d, 기대 %d (바디 %s)", r.status, want, r.body)
	}
}

func assertMinutes(t *testing.T, r response, want float64) {
	t.Helper()
	assertStatus(t, r, http.StatusOK)
	if got := r.minutes(t); !approx(got, want, 1e-6) {
		t.Fatalf("minutes %v, 기대 %v", got, want)
	}
}

// ---------- /health ----------

// /health 는 200 과 status=ok, 현재 구현체 이름을 JSON 으로 돌려준다.
func TestHealthReturnsOKAndModelName(t *testing.T) {
	r := do(t, rulePredictor{}, http.MethodGet, "/health", "", "")
	assertStatus(t, r, http.StatusOK)
	if got := r.header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type %q", got)
	}
	want := map[string]any{"status": "ok", "model": testModel}
	if got := r.json(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("바디 %v, 기대 %v", got, want)
	}
}

// ---------- /eta 정상 입력 ----------

// /eta 응답은 minutes(숫자)와 model(문자열) 두 필드만 가진다.
func TestETAResponseShape(t *testing.T) {
	r := postETA(t, eta("2.0", "3"))
	assertStatus(t, r, http.StatusOK)
	body := r.json(t)
	if len(body) != 2 {
		t.Fatalf("필드가 minutes, model 두 개가 아니다: %v", body)
	}
	if _, ok := body["minutes"].(float64); !ok {
		t.Fatalf("minutes 가 숫자가 아니다: %v", body)
	}
	if body["model"] != testModel {
		t.Fatalf("model %v, 기대 %q", body["model"], testModel)
	}
}

// 알려진 입력에 대해 예측기 결과(10 + 3*거리 + 2*대기주문)를 그대로 돌려준다.
func TestETAKnownValues(t *testing.T) {
	cases := []struct {
		d, p string
		want float64
	}{
		{"2.0", "3", 22},   // 기본
		{"1.5", "0", 14.5}, // 소수 거리
		{"5", "1", 27},     // 정수로 보낸 거리
	}
	for _, c := range cases {
		t.Run(c.d+"_"+c.p, func(t *testing.T) { assertMinutes(t, postETA(t, eta(c.d, c.p)), c.want) })
	}
}

// application/json; charset=utf-8 과 application/*+json 도 JSON 으로 받는다.
func TestETAAcceptsJSONContentTypeVariants(t *testing.T) {
	for _, ct := range []string{"application/json; charset=utf-8", "application/vnd.api+json"} {
		t.Run(ct, func(t *testing.T) {
			assertMinutes(t, do(t, rulePredictor{}, http.MethodPost, "/eta", eta("1", "0"), ct), 13)
		})
	}
}

// ---------- /eta 경계값 ----------

// 거리 0, 대기 주문 0 이면 기본 10분을 돌려준다.
func TestETAZeroInputs(t *testing.T) {
	assertMinutes(t, postETA(t, eta("0", "0")), 10)
}

// -0.0 거리는 0 과 같게 취급해 200 을 돌려준다.
func TestETANegativeZeroDistance(t *testing.T) {
	assertMinutes(t, postETA(t, eta("-0.0", "0")), 10)
}

// 큰 거리와 많은 대기 주문도 200 으로 처리한다.
func TestETALargeValues(t *testing.T) {
	assertMinutes(t, postETA(t, eta("1000000.0", "1000000")), 10+3_000_000+2_000_000)
}

// 아주 큰 정수 대기 주문 수(10^30)도 200 과 유한한 minutes 를 돌려준다.
func TestETAHugeIntegerOrders(t *testing.T) {
	r := postETA(t, eta("1", "1000000000000000000000000000000"))
	assertStatus(t, r, http.StatusOK)
	// float32 로 바꿔 모델에 넣으므로 상대 오차로 비교한다.
	if got := r.minutes(t); !approx(got, 2e30, 1e-6) {
		t.Fatalf("minutes %v, 기대 약 2e30", got)
	}
}

// float32 로 표현할 수 있는 최대 근처 거리는 받고, 넘는 값(1e39)은 422 로 거절한다.
func TestETAFloat32RangeBoundary(t *testing.T) {
	assertStatus(t, postETA(t, eta("3.4e38", "0")), http.StatusOK)
	r := postETA(t, eta("1e39", "0"))
	assertStatus(t, r, http.StatusUnprocessableEntity)
	if got := r.locs(t); !reflect.DeepEqual(got, []string{"body.distance_km"}) {
		t.Fatalf("loc %v", got)
	}
}

// 0 바로 아래 음수 거리(-1e-9)는 422 를 돌려준다.
func TestETATinyNegativeDistance(t *testing.T) {
	assertStatus(t, postETA(t, eta("-1e-9", "0")), http.StatusUnprocessableEntity)
}

// ---------- /eta 잘못된 입력 → 422 ----------

// 음수·숫자가 아닌 값·정수가 아닌 값·null·배열·객체·누락은 422 이고, loc 이 문제 필드를 가리킨다.
func TestETAInvalidFieldsReturn422(t *testing.T) {
	cases := []struct {
		name, body string
		locs       []string
	}{
		{"음수 거리", eta("-1.0", "0"), []string{"body.distance_km"}},
		{"음수 대기 주문", eta("1.0", "-1"), []string{"body.pending_orders"}},
		{"숫자가 아닌 거리", eta(`"abc"`, "0"), []string{"body.distance_km"}},
		{"숫자가 아닌 대기 주문", eta("1.0", `"many"`), []string{"body.pending_orders"}},
		{"정수가 아닌 대기 주문", eta("1.0", "1.5"), []string{"body.pending_orders"}},
		{"정수가 아닌 숫자 문자열", eta("1.0", `"2.5"`), []string{"body.pending_orders"}},
		{"지수 표기 정수 문자열", eta("1.0", `"1e2"`), []string{"body.pending_orders"}},
		{"int64 를 넘는 float 대기 주문", eta("1.0", "1e30"), []string{"body.pending_orders"}},
		{"null 거리", eta("null", "0"), []string{"body.distance_km"}},
		{"빈 문자열 거리", eta(`""`, "0"), []string{"body.distance_km"}},
		{"16진 문자열 거리", eta(`"0x10"`, "0"), []string{"body.distance_km"}},
		{"배열 거리", eta("[1]", "0"), []string{"body.distance_km"}},
		{"객체 거리", eta(`{"v": 1}`, "0"), []string{"body.distance_km"}},
		{"거리 누락", `{"pending_orders": 0}`, []string{"body.distance_km"}},
		{"대기 주문 누락", `{"distance_km": 1.0}`, []string{"body.pending_orders"}},
		{"빈 객체", `{}`, []string{"body.distance_km", "body.pending_orders"}},
		{"두 필드 모두 잘못됨", eta(`"abc"`, "-1"), []string{"body.distance_km", "body.pending_orders"}},
		{"중복 키는 마지막 값", `{"distance_km": 1, "distance_km": -1, "pending_orders": 0}`, []string{"body.distance_km"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := postETA(t, c.body)
			assertStatus(t, r, http.StatusUnprocessableEntity)
			if got := r.locs(t); !reflect.DeepEqual(got, c.locs) {
				t.Fatalf("loc %v, 기대 %v", got, c.locs)
			}
		})
	}
}

// NaN·무한대·float64 범위를 넘는 거리(리터럴과 문자열 모두)는 422 를 돌려준다.
func TestETANonFiniteDistanceReturns422(t *testing.T) {
	for _, d := range []string{"NaN", "Infinity", "1e400", `"inf"`, `"nan"`, `"-Infinity"`, `"1e400"`} {
		t.Run(d, func(t *testing.T) { assertStatus(t, postETA(t, eta(d, "0")), http.StatusUnprocessableEntity) })
	}
}

// JSON 이 아니거나 객체가 아닌 바디, 뒤에 데이터가 남은 바디는 422 와 loc=body 를 돌려준다.
func TestETAMalformedBodyReturns422(t *testing.T) {
	for _, body := range []string{"not json", "", "   ", "[1, 2]", "null", `"text"`, "3",
		eta("1", "0") + " trailing"} {
		t.Run(body, func(t *testing.T) {
			r := postETA(t, body)
			assertStatus(t, r, http.StatusUnprocessableEntity)
			if got := r.locs(t); !reflect.DeepEqual(got, []string{"body"}) {
				t.Fatalf("loc %v", got)
			}
		})
	}
}

// Content-Type 이 없거나 JSON 이 아니면 바디가 올바른 JSON 이어도 422 를 돌려준다.
func TestETANonJSONContentTypeReturns422(t *testing.T) {
	for _, ct := range []string{"", "text/plain", "application/x-json"} {
		t.Run(ct, func(t *testing.T) {
			r := do(t, rulePredictor{}, http.MethodPost, "/eta", eta("1", "0"), ct)
			assertStatus(t, r, http.StatusUnprocessableEntity)
		})
	}
}

// ---------- /eta 느슨한 타입 변환 (Python 현재 정책과 같게) ----------

// 숫자 문자열·소수부 0 float·bool 은 숫자로 바꿔 계산한다.
func TestETALaxCoercion(t *testing.T) {
	cases := []struct {
		name, d, p string
		want       float64
	}{
		{"숫자 문자열 거리", `"2"`, "0", 16},
		{"공백 있는 숫자 문자열 거리", `" 2 "`, "0", 16},
		{"지수 표기 문자열 거리", `"2.5e1"`, "0", 85},
		{"밑줄 있는 문자열 거리", `"1_000"`, "0", 3010},
		{"소수점으로 시작하는 문자열 거리", `".5"`, "0", 11.5},
		{"숫자 문자열 대기 주문", "1", `"3"`, 19},
		{"소수부 0 문자열 대기 주문", "1", `"3.0"`, 19},
		{"부호 있는 문자열 대기 주문", "1", `"+3"`, 19},
		{"소수부 0 float 대기 주문", "1", "2.0", 17},
		{"지수 표기 float 대기 주문", "1", "1e2", 213},
		{"bool 거리", "true", "0", 13},
		{"bool 대기 주문", "1", "true", 15},
		{"false 는 0", "false", "false", 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { assertMinutes(t, postETA(t, eta(c.d, c.p)), c.want) })
	}
}

// 정의되지 않은 필드는 무시하고 200 을 돌려준다.
func TestETAExtraFieldIgnored(t *testing.T) {
	assertMinutes(t, postETA(t, `{"distance_km": 1, "pending_orders": 0, "x": 1}`), 13)
}

// ---------- /eta 예측 실패 → 500 ----------

// 예측기가 오류를 내거나 유한하지 않은 값을 내면 minutes=null 대신 500 을 돌려준다.
func TestETAPredictorFailureReturns500(t *testing.T) {
	cases := map[string]fixedPredictor{
		"오류":  {err: errors.New("boom")},
		"NaN": {minutes: math.NaN()},
		"inf": {minutes: math.Inf(1)},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			r := do(t, p, http.MethodPost, "/eta", eta("1", "0"), "application/json")
			assertStatus(t, r, http.StatusInternalServerError)
			if r.json(t)["detail"] == nil {
				t.Fatalf("detail 이 없다: %s", r.body)
			}
		})
	}
}

// ---------- 라우팅 ----------

// 정해진 메서드가 아니면 405, 정의되지 않은 경로는 404 를 돌려준다.
func TestRouting(t *testing.T) {
	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/eta", http.StatusMethodNotAllowed},
		{http.MethodPost, "/health", http.StatusMethodNotAllowed},
		{http.MethodGet, "/nope", http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			assertStatus(t, do(t, rulePredictor{}, c.method, c.path, "", ""), c.want)
		})
	}
}

// ---------- 실제 ONNX 모델 ----------

// 실제 모델은 로드되고, 알려진 입력에 대해 유한한 양수 minutes 를 돌려주며 거리·주문이 늘면 늘어난다.
func TestONNXPredictor(t *testing.T) {
	lib, model := os.Getenv("ONNXRUNTIME_LIB"), os.Getenv("MODEL_PATH")
	for _, f := range []string{lib, model} {
		if _, err := os.Stat(f); f == "" || err != nil {
			t.Skip("ONNXRUNTIME_LIB, MODEL_PATH 가 가리키는 파일이 없어 건너뛴다")
		}
	}
	p, err := newONNXPredictor(lib, model)
	if err != nil {
		t.Fatal(err)
	}
	predict := func(d, n float32) float64 {
		t.Helper()
		m, err := p.Predict(d, n)
		if err != nil {
			t.Fatal(err)
		}
		if math.IsNaN(m) || math.IsInf(m, 0) || m <= 0 {
			t.Fatalf("Predict(%v, %v) = %v, 유한한 양수가 아니다", d, n, m)
		}
		return m
	}
	base := predict(3, 2)
	if predict(6, 2) <= base || predict(3, 8) <= base {
		t.Fatalf("거리나 대기 주문이 늘었는데 예상 시간이 늘지 않았다")
	}
	predict(0, 0)

	// HTTP 를 거쳐도 같은 값이 나온다.
	assertMinutes(t, do(t, p, http.MethodPost, "/eta", eta("3", "2"), "application/json"), base)
}
