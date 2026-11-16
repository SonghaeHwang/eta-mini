package main

// HTTP 계층. api/app.py 와 같은 응답 형식과 상태 코드를 낸다.
//
//	GET  /health  200 {"status": "ok", "model": <구현체 이름>}
//	POST /eta     200 {"minutes": <유한한 숫자>, "model": <구현체 이름>}
//	              422 {"detail": [{"type", "loc", "msg", "input", ...}]}  잘못된 입력
//	              500 {"detail": "Internal Server Error"}                   예측 실패
//	그 밖의 메서드는 405, 정의되지 않은 경로는 404.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"mime"
	"net/http"
	"strings"
)

// Predictor 는 거리(km)와 대기 주문 수로 배달 예상 시간(분)을 계산한다.
// 운영에서는 onnxPredictor 를 쓰고, 테스트에서는 가짜 구현을 넣는다.
type Predictor interface {
	Predict(distanceKm, pendingOrders float32) (float64, error)
}

type etaResponse struct {
	Minutes float64 `json:"minutes"`
	Model   string  `json:"model"`
}

const maxBodyBytes = 1 << 20

func newMux(p Predictor, modelImpl string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler(modelImpl))
	mux.HandleFunc("POST /eta", etaHandler(p, modelImpl))
	return mux
}

func healthHandler(modelImpl string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "model": modelImpl})
	}
}

func etaHandler(p Predictor, modelImpl string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"detail": "요청 바디가 너무 크다"})
			return
		}
		if !isJSONContentType(r.Header.Get("Content-Type")) {
			// FastAPI 는 JSON 이 아닌 바디를 객체로 보지 않아 422 를 낸다.
			writeValidationErrors(w, []fieldError{{Type: "model_attributes_type", Loc: []any{"body"},
				Msg: "Input should be a valid dictionary or object to extract fields from", Input: string(raw)}})
			return
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			writeValidationErrors(w, []fieldError{{Type: "missing", Loc: []any{"body"}, Msg: "Field required", Input: nil}})
			return
		}
		body, err := decodeJSON(raw)
		if err != nil {
			writeValidationErrors(w, []fieldError{{Type: "json_invalid", Loc: []any{"body"},
				Msg: "JSON decode error", Input: map[string]any{}, Ctx: map[string]any{"error": err.Error()}}})
			return
		}
		req, errs := parseEtaRequest(body)
		if len(errs) > 0 {
			writeValidationErrors(w, errs)
			return
		}
		minutes, err := p.Predict(req.distanceKm, req.pendingOrders)
		if err != nil {
			log.Printf("예측 실패: %v", err)
			writeInternalError(w)
			return
		}
		if math.IsNaN(minutes) || math.IsInf(minutes, 0) {
			// 응답 minutes 는 항상 유한한 숫자여야 한다. 모델 출력이 float32 범위를 넘으면 여기로 온다.
			log.Printf("유한하지 않은 예측값: %v (입력 %v, %v)", minutes, req.distanceKm, req.pendingOrders)
			writeInternalError(w)
			return
		}
		writeJSON(w, http.StatusOK, etaResponse{Minutes: minutes, Model: modelImpl})
	}
}

// isJSONContentType 은 FastAPI 와 같이 application/json 과 application/*+json 을 JSON 으로 본다.
func isJSONContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	typ, sub, ok := strings.Cut(mt, "/")
	return ok && typ == "application" && (sub == "json" || strings.HasSuffix(sub, "+json"))
}

// decodeJSON 은 바디 전체가 JSON 값 하나인지 확인하며 디코드한다. 숫자는 원문 그대로 둔다.
func decodeJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON 값 뒤에 남은 데이터가 있다")
	}
	return v, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("응답 쓰기 실패: %v", err)
	}
}

func writeValidationErrors(w http.ResponseWriter, errs []fieldError) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": errs})
}

func writeInternalError(w http.ResponseWriter) {
	writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": "Internal Server Error"})
}
