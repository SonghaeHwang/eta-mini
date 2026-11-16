package main

// POST /eta 요청 검증. api/app.py 의 EtaRequest(Pydantic lax 모드)와 같은 입력을 받고 같은 입력을 거절한다.
//
//	distance_km    float, >= 0, NaN·inf 불가. 숫자 문자열과 bool 도 숫자로 변환한다.
//	pending_orders int,   >= 0. 숫자 문자열, 소수부가 0 인 float, bool 도 정수로 변환한다.
//	정의되지 않은 필드는 무시한다. 같은 키가 여러 번 오면 마지막 값을 쓴다.
//
// 예외 하나: 모델 입력이 float32 라서, float32 로 표현할 수 없는 값(약 3.4e38 초과)은 422 로 거절한다.

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// fieldError 는 FastAPI 422 응답의 detail 항목 하나와 같은 모양이다.
type fieldError struct {
	Type  string         `json:"type"`
	Loc   []any          `json:"loc"`
	Msg   string         `json:"msg"`
	Input any            `json:"input"`
	Ctx   map[string]any `json:"ctx,omitempty"`
}

type etaRequest struct {
	distanceKm    float32
	pendingOrders float32
}

var (
	// Python float() 이 받는 10진 표기: 앞뒤 공백 제거 후, 숫자 사이 밑줄 허용, ".5" "5." 허용.
	floatStrRe = regexp.MustCompile(`^[+-]?(?:\d(?:_?\d)*(?:\.(?:\d(?:_?\d)*)?)?|\.\d(?:_?\d)*)(?:[eE][+-]?\d(?:_?\d)*)?$`)
	// inf·nan 표기. 파싱은 되지만 finite 검사에서 거절된다.
	nonFiniteStrRe = regexp.MustCompile(`(?i)^[+-]?(?:inf|infinity|nan)$`)
	// 정수 문자열: 숫자 사이 밑줄 허용, 소수부가 0 뿐이면 허용("3.0"). "3." 은 거절.
	intStrRe = regexp.MustCompile(`^[+-]?\d(?:_?\d)*(?:\.0+)?$`)
)

const int64Limit = 9223372036854775808.0 // 2^63

func parseEtaRequest(body any) (etaRequest, []fieldError) {
	obj, ok := body.(map[string]any)
	if !ok {
		return etaRequest{}, []fieldError{{Type: "model_attributes_type", Loc: []any{"body"},
			Msg: "Input should be a valid dictionary or object to extract fields from", Input: body}}
	}
	var req etaRequest
	var errs []fieldError

	if v, present := obj["distance_km"]; !present {
		errs = append(errs, missing("distance_km", obj))
	} else if d, fe := parseDistance(v); fe != nil {
		errs = append(errs, *fe)
	} else {
		req.distanceKm = d
	}

	if v, present := obj["pending_orders"]; !present {
		errs = append(errs, missing("pending_orders", obj))
	} else if n, fe := parsePendingOrders(v); fe != nil {
		errs = append(errs, *fe)
	} else {
		req.pendingOrders = n
	}
	return req, errs
}

func missing(field string, obj map[string]any) fieldError {
	return fieldError{Type: "missing", Loc: []any{"body", field}, Msg: "Field required", Input: obj}
}

func fail(field, typ, msg string, input any, ctx map[string]any) *fieldError {
	return &fieldError{Type: typ, Loc: []any{"body", field}, Msg: msg, Input: input, Ctx: ctx}
}

func parseDistance(v any) (float32, *fieldError) {
	const field = "distance_km"
	var f float64
	switch x := v.(type) {
	case json.Number:
		var err error
		f, err = strconv.ParseFloat(string(x), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) { // 범위 초과(1e400)는 inf 가 되어 아래에서 거절
			return 0, fail(field, "float_parsing", "Input should be a valid number, unable to parse string as a number", v, nil)
		}
	case string:
		s := strings.TrimSpace(x)
		switch {
		case floatStrRe.MatchString(s):
			f, _ = strconv.ParseFloat(strings.ReplaceAll(s, "_", ""), 64)
		case nonFiniteStrRe.MatchString(s):
			f = math.NaN()
		default:
			return 0, fail(field, "float_parsing", "Input should be a valid number, unable to parse string as a number", v, nil)
		}
	case bool:
		f = boolToFloat(x)
	default:
		return 0, fail(field, "float_type", "Input should be a valid number", v, nil)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fail(field, "finite_number", "Input should be a finite number", v, nil)
	}
	if f < 0 {
		return 0, fail(field, "greater_than_equal", "Input should be greater than or equal to 0", v, map[string]any{"ge": 0})
	}
	return toFloat32(field, f, v)
}

func parsePendingOrders(v any) (float32, *fieldError) {
	const field = "pending_orders"
	intParsing := func() *fieldError {
		return fail(field, "int_parsing", "Input should be a valid integer, unable to parse string as an integer", v, nil)
	}
	var n *big.Int
	switch x := v.(type) {
	case json.Number:
		s := string(x)
		if !strings.ContainsAny(s, ".eE") {
			// 정수 표기는 크기 제한 없이 받는다 (Python int 와 같다).
			n, _ = new(big.Int).SetString(s, 10)
			break
		}
		f, err := strconv.ParseFloat(s, 64)
		if (err != nil && !errors.Is(err, strconv.ErrRange)) || math.IsInf(f, 0) || math.Abs(f) >= int64Limit {
			// Pydantic 은 float 표기를 int64 범위에서만 정수로 바꾼다 (1e30 → 422).
			return 0, fail(field, "int_from_float", "Input should be a valid integer, got a number with a fractional part", v, nil)
		}
		if f != math.Trunc(f) {
			return 0, fail(field, "int_from_float", "Input should be a valid integer, got a number with a fractional part", v, nil)
		}
		n, _ = new(big.Float).SetFloat64(f).Int(nil)
	case string:
		s := strings.TrimSpace(x)
		if !intStrRe.MatchString(s) {
			return 0, intParsing()
		}
		s, _, _ = strings.Cut(s, ".")
		var ok bool
		if n, ok = new(big.Int).SetString(strings.ReplaceAll(s, "_", ""), 10); !ok {
			return 0, intParsing()
		}
	case bool:
		n = big.NewInt(int64(boolToFloat(x)))
	default:
		return 0, fail(field, "int_type", "Input should be a valid integer", v, nil)
	}
	if n == nil {
		return 0, intParsing()
	}
	if n.Sign() < 0 {
		return 0, fail(field, "greater_than_equal", "Input should be greater than or equal to 0", v, map[string]any{"ge": 0})
	}
	f, _ := new(big.Float).SetInt(n).Float64()
	return toFloat32(field, f, v)
}

func toFloat32(field string, f float64, input any) (float32, *fieldError) {
	f32 := float32(f)
	if math.IsInf(float64(f32), 0) {
		return 0, fail(field, "less_than_equal", "Input should be representable as float32 (model input)", input,
			map[string]any{"le": math.MaxFloat32})
	}
	return f32, nil
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
