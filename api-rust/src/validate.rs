//! POST /eta 요청 검증. api/app.py 의 EtaRequest(Pydantic lax 모드)와 같은 입력을 받고 같은 입력을 거절한다.
//!
//!   distance_km    float, >= 0, NaN·inf 불가. 숫자 문자열과 bool 도 숫자로 변환한다.
//!   pending_orders int,   >= 0. 숫자 문자열, 소수부가 0 인 float, bool 도 정수로 변환한다.
//!   정의되지 않은 필드는 무시한다. 같은 키가 여러 번 오면 마지막 값을 쓴다.
//!
//! 예외 하나: 모델 입력이 float32 라서, float32 로 표현할 수 없는 값(약 3.4e38 초과)은 422 로 거절한다.

use std::sync::LazyLock;

use regex::Regex;
use serde::Serialize;
use serde_json::{Map, Value, json};

/// FastAPI 422 응답의 detail 항목 하나와 같은 모양이다.
#[derive(Debug, Serialize)]
pub struct FieldError {
    #[serde(rename = "type")]
    pub kind: &'static str,
    pub loc: Vec<Value>,
    pub msg: &'static str,
    pub input: Value,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ctx: Option<Value>,
}

impl FieldError {
    pub fn body(kind: &'static str, msg: &'static str, input: Value, ctx: Option<Value>) -> Self {
        FieldError { kind, loc: vec![json!("body")], msg, input, ctx }
    }

    fn field(field: &str, kind: &'static str, msg: &'static str, input: &Value, ctx: Option<Value>) -> Self {
        FieldError { kind, loc: vec![json!("body"), json!(field)], msg, input: input.clone(), ctx }
    }
}

#[derive(Debug, PartialEq)]
pub struct EtaRequest {
    pub distance_km: f32,
    pub pending_orders: f32,
}

// Python float() 이 받는 10진 표기: 앞뒤 공백 제거 후, 숫자 사이 밑줄 허용, ".5" "5." 허용.
static FLOAT_STR: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^[+-]?(?:[0-9](?:_?[0-9])*(?:\.(?:[0-9](?:_?[0-9])*)?)?|\.[0-9](?:_?[0-9])*)(?:[eE][+-]?[0-9](?:_?[0-9])*)?$").unwrap()
});
// inf·nan 표기. 파싱은 되지만 finite 검사에서 거절된다.
static NON_FINITE_STR: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"(?i)^[+-]?(?:inf|infinity|nan)$").unwrap());
// 정수 문자열: 숫자 사이 밑줄 허용, 소수부가 0 뿐이면 허용("3.0"). "3." 은 거절.
static INT_STR: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"^[+-]?[0-9](?:_?[0-9])*(?:\.0+)?$").unwrap());

const INT64_LIMIT: f64 = 9223372036854775808.0; // 2^63

const MSG_FLOAT_PARSING: &str = "Input should be a valid number, unable to parse string as a number";
const MSG_INT_PARSING: &str = "Input should be a valid integer, unable to parse string as an integer";
const MSG_INT_FROM_FLOAT: &str = "Input should be a valid integer, got a number with a fractional part";
const MSG_GE: &str = "Input should be greater than or equal to 0";

pub fn parse_eta_request(body: &Value) -> Result<EtaRequest, Vec<FieldError>> {
    let Some(obj) = body.as_object() else {
        return Err(vec![FieldError::body(
            "model_attributes_type",
            "Input should be a valid dictionary or object to extract fields from",
            body.clone(),
            None,
        )]);
    };
    let mut errs = Vec::new();

    let distance = match obj.get("distance_km") {
        None => Err(missing("distance_km", obj)),
        Some(v) => parse_distance(v),
    };
    let pending = match obj.get("pending_orders") {
        None => Err(missing("pending_orders", obj)),
        Some(v) => parse_pending_orders(v),
    };
    match (distance, pending) {
        (Ok(distance_km), Ok(pending_orders)) => Ok(EtaRequest { distance_km, pending_orders }),
        (d, p) => {
            errs.extend(d.err());
            errs.extend(p.err());
            Err(errs)
        }
    }
}

fn missing(field: &str, obj: &Map<String, Value>) -> FieldError {
    FieldError::field(field, "missing", "Field required", &Value::Object(obj.clone()), None)
}

fn ge_zero(field: &str, input: &Value) -> FieldError {
    FieldError::field(field, "greater_than_equal", MSG_GE, input, Some(json!({"ge": 0})))
}

fn parse_distance(v: &Value) -> Result<f32, FieldError> {
    const FIELD: &str = "distance_km";
    let parsing = || FieldError::field(FIELD, "float_parsing", MSG_FLOAT_PARSING, v, None);
    let f: f64 = match v {
        // arbitrary_precision 이라 원문이 남아 있다. 범위를 넘는 값(1e400)은 inf 가 되어 아래에서 거절.
        Value::Number(n) => n.to_string().parse().map_err(|_| parsing())?,
        Value::String(s) => {
            let s = s.trim();
            if FLOAT_STR.is_match(s) {
                s.replace('_', "").parse().map_err(|_| parsing())?
            } else if NON_FINITE_STR.is_match(s) {
                f64::NAN
            } else {
                return Err(parsing());
            }
        }
        Value::Bool(b) => bool_to_f64(*b),
        _ => return Err(FieldError::field(FIELD, "float_type", "Input should be a valid number", v, None)),
    };
    if !f.is_finite() {
        return Err(FieldError::field(FIELD, "finite_number", "Input should be a finite number", v, None));
    }
    if f < 0.0 {
        return Err(ge_zero(FIELD, v));
    }
    to_f32(FIELD, f, v)
}

fn parse_pending_orders(v: &Value) -> Result<f32, FieldError> {
    const FIELD: &str = "pending_orders";
    let parsing = || FieldError::field(FIELD, "int_parsing", MSG_INT_PARSING, v, None);
    let from_float = || FieldError::field(FIELD, "int_from_float", MSG_INT_FROM_FLOAT, v, None);
    // 정수는 크기 제한 없이 받는다 (Python int 와 같다). 크기와 부호만 필요하므로 f64 로 충분하다:
    // f64 를 넘는 정수는 inf 가 되어 float32 검사에서 거절된다.
    let n: f64 = match v {
        Value::Number(num) => {
            let s = num.to_string();
            let f: f64 = s.parse().map_err(|_| parsing())?;
            if s.contains(['.', 'e', 'E']) {
                // Pydantic 은 float 표기를 int64 범위에서만 정수로 바꾼다 (1e30 → 422).
                if !f.is_finite() || f.abs() >= INT64_LIMIT || f != f.trunc() {
                    return Err(from_float());
                }
            }
            f
        }
        Value::String(s) => {
            let s = s.trim();
            if !INT_STR.is_match(s) {
                return Err(parsing());
            }
            let int_part = s.split('.').next().unwrap_or(s);
            int_part.replace('_', "").parse().map_err(|_| parsing())?
        }
        Value::Bool(b) => bool_to_f64(*b),
        _ => return Err(FieldError::field(FIELD, "int_type", "Input should be a valid integer", v, None)),
    };
    if n < 0.0 {
        return Err(ge_zero(FIELD, v));
    }
    to_f32(FIELD, n, v)
}

fn to_f32(field: &str, f: f64, input: &Value) -> Result<f32, FieldError> {
    let f32v = f as f32;
    if f32v.is_infinite() {
        return Err(FieldError::field(
            field,
            "less_than_equal",
            "Input should be representable as float32 (model input)",
            input,
            Some(json!({"le": f32::MAX as f64})),
        ));
    }
    Ok(f32v)
}

fn bool_to_f64(b: bool) -> f64 {
    if b { 1.0 } else { 0.0 }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn parse(s: &str) -> Result<EtaRequest, Vec<&'static str>> {
        let v: Value = serde_json::from_str(s).unwrap();
        parse_eta_request(&v).map_err(|es| es.into_iter().map(|e| e.kind).collect())
    }

    #[test]
    fn accepts_valid_and_coerced_inputs() {
        assert_eq!(parse(r#"{"distance_km": 3.0, "pending_orders": 2}"#), Ok(EtaRequest { distance_km: 3.0, pending_orders: 2.0 }));
        assert_eq!(parse(r#"{"distance_km": "1_000.5", "pending_orders": " 3.00 "}"#), Ok(EtaRequest { distance_km: 1000.5, pending_orders: 3.0 }));
        assert_eq!(parse(r#"{"distance_km": true, "pending_orders": 4.0, "x": 1}"#), Ok(EtaRequest { distance_km: 1.0, pending_orders: 4.0 }));
        assert_eq!(parse(r#"{"distance_km": 0, "pending_orders": 0}"#), Ok(EtaRequest { distance_km: 0.0, pending_orders: 0.0 }));
    }

    #[test]
    fn rejects_invalid_inputs() {
        assert_eq!(parse(r#"[]"#), Err(vec!["model_attributes_type"]));
        assert_eq!(parse(r#"{}"#), Err(vec!["missing", "missing"]));
        assert_eq!(parse(r#"{"distance_km": -1, "pending_orders": -1}"#), Err(vec!["greater_than_equal", "greater_than_equal"]));
        assert_eq!(parse(r#"{"distance_km": "abc", "pending_orders": "3."}"#), Err(vec!["float_parsing", "int_parsing"]));
        assert_eq!(parse(r#"{"distance_km": "nan", "pending_orders": 2.5}"#), Err(vec!["finite_number", "int_from_float"]));
        assert_eq!(parse(r#"{"distance_km": 1e400, "pending_orders": 1e30}"#), Err(vec!["finite_number", "int_from_float"]));
        assert_eq!(parse(r#"{"distance_km": null, "pending_orders": [1]}"#), Err(vec!["float_type", "int_type"]));
        assert_eq!(parse(r#"{"distance_km": 1e39, "pending_orders": 1000000000000000000000000000000000000000}"#), Err(vec!["less_than_equal", "less_than_equal"]));
    }
}
