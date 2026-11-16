//! delivery-eta 의 Rust 구현. api/app.py 와 같은 계약(POST /eta, GET /health)을 따른다.
//!
//! 예측은 ONNX 모델(입력 "X" [1, 2] float32 → 출력 "variable")을 ONNX Runtime 으로 실행한다.
//! 정규화는 모델 안에 있으므로 원래 단위의 값을 그대로 넣는다.
//!
//! axum 의 Json 추출기는 형식 오류에 400·415 를 돌려주므로 쓰지 않고, 본문을 직접 파싱해
//! 모든 입력 오류를 FastAPI 와 같은 422 로 맞춘다.
//!
//! 환경 변수
//!   PORT        듣는 포트 (기본 8080)
//!   MODEL_PATH  ONNX 모델 경로 (기본 artifacts/model_sklearn.onnx)
//!   ORT_LIB     libonnxruntime.so 경로 (기본 /usr/local/lib/libonnxruntime.so)
//!   MODEL_IMPL  응답의 model 값 (기본 onnx-rust)

mod validate;

use std::{process::ExitCode, sync::Arc, sync::Mutex};

use axum::{
    Router,
    body::{Body, Bytes},
    extract::State,
    http::{HeaderMap, StatusCode, header},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use ort::{environment::Environment, session::Session, value::Tensor};
use serde_json::{Value, json};

use validate::{FieldError, parse_eta_request};

const MAX_BODY_BYTES: usize = 1 << 20;

fn getenv(key: &str, fallback: &str) -> String {
    std::env::var(key).ok().filter(|v| !v.is_empty()).unwrap_or_else(|| fallback.to_string())
}

/// ONNX 세션 하나를 감싼다. ort 의 Session::run 은 &mut self 라 Mutex 로 직렬화한다
/// (모델이 아주 작아 실행 시간이 마이크로초 단위이므로 경합은 문제되지 않는다).
struct Predictor {
    _env: Environment,
    session: Mutex<Session>,
}

impl Predictor {
    fn new(lib_path: &str, model_path: &str) -> Result<Self, String> {
        let env = ort::init_from(lib_path)
            .map_err(|e| format!("ONNX Runtime 로드 실패 ({lib_path}): {e}"))?
            .with_name("delivery-eta")
            .with_telemetry(false)
            .build()
            .map_err(|e| format!("ONNX Runtime 초기화 실패: {e}"))?;
        let session = Session::builder(&env)
            .and_then(|mut b| b.commit_from_file(model_path))
            .map_err(|e| format!("모델 로드 실패 ({model_path}): {e}"))?;
        Ok(Predictor { _env: env, session: Mutex::new(session) })
    }

    fn predict(&self, distance_km: f32, pending_orders: f32) -> ort::Result<f64> {
        let input = Tensor::from_array(([1usize, 2], vec![distance_km, pending_orders]))?;
        let mut session = self.session.lock().unwrap_or_else(|e| e.into_inner());
        let outputs = session.run(ort::inputs!["X" => input])?;
        let (_, data) = outputs["variable"].try_extract_tensor::<f32>()?;
        Ok(f64::from(data[0]))
    }
}

struct AppState {
    predictor: Predictor,
    model_impl: String,
}

fn json_response(status: StatusCode, v: Value) -> Response {
    (status, [(header::CONTENT_TYPE, "application/json")], v.to_string()).into_response()
}

fn validation_errors(errs: Vec<FieldError>) -> Response {
    json_response(StatusCode::UNPROCESSABLE_ENTITY, json!({ "detail": errs }))
}

fn internal_error() -> Response {
    json_response(StatusCode::INTERNAL_SERVER_ERROR, json!({ "detail": "Internal Server Error" }))
}

/// FastAPI 와 같이 application/json 과 application/*+json 을 JSON 으로 본다.
fn is_json_content_type(headers: &HeaderMap) -> bool {
    let Some(ct) = headers.get(header::CONTENT_TYPE).and_then(|v| v.to_str().ok()) else {
        return false;
    };
    let mt = ct.split(';').next().unwrap_or("").trim().to_ascii_lowercase();
    match mt.split_once('/') {
        Some(("application", sub)) => sub == "json" || (sub.ends_with("+json") && sub.len() > 5),
        _ => false,
    }
}

async fn health(State(st): State<Arc<AppState>>) -> Response {
    json_response(StatusCode::OK, json!({ "status": "ok", "model": st.model_impl }))
}

async fn eta(State(st): State<Arc<AppState>>, headers: HeaderMap, body: Body) -> Response {
    let raw: Bytes = match axum::body::to_bytes(body, MAX_BODY_BYTES).await {
        Ok(b) => b,
        Err(_) => return json_response(StatusCode::PAYLOAD_TOO_LARGE, json!({ "detail": "요청 바디가 너무 크다" })),
    };
    if !is_json_content_type(&headers) {
        // FastAPI 는 JSON 이 아닌 바디를 객체로 보지 않아 422 를 낸다.
        return validation_errors(vec![FieldError::body(
            "model_attributes_type",
            "Input should be a valid dictionary or object to extract fields from",
            Value::String(String::from_utf8_lossy(&raw).into_owned()),
            None,
        )]);
    }
    if raw.trim_ascii().is_empty() {
        return validation_errors(vec![FieldError::body("missing", "Field required", Value::Null, None)]);
    }
    let body: Value = match serde_json::from_slice(&raw) {
        Ok(v) => v,
        Err(e) => {
            return validation_errors(vec![FieldError::body(
                "json_invalid",
                "JSON decode error",
                json!({}),
                Some(json!({ "error": e.to_string() })),
            )]);
        }
    };
    let req = match parse_eta_request(&body) {
        Ok(r) => r,
        Err(errs) => return validation_errors(errs),
    };
    let minutes = match st.predictor.predict(req.distance_km, req.pending_orders) {
        Ok(m) => m,
        Err(e) => {
            eprintln!("예측 실패: {e}");
            return internal_error();
        }
    };
    if !minutes.is_finite() {
        // 응답 minutes 는 항상 유한한 숫자여야 한다. 모델이 float32 범위를 넘으면 여기로 온다.
        eprintln!("유한하지 않은 예측값: {minutes} (입력 {}, {})", req.distance_km, req.pending_orders);
        return internal_error();
    }
    json_response(StatusCode::OK, json!({ "minutes": minutes, "model": st.model_impl }))
}

fn app(state: Arc<AppState>) -> Router {
    Router::new().route("/health", get(health)).route("/eta", post(eta)).with_state(state)
}

async fn shutdown_signal() {
    let ctrl_c = async {
        let _ = tokio::signal::ctrl_c().await;
    };
    #[cfg(unix)]
    let term = async {
        if let Ok(mut s) = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
            s.recv().await;
        }
    };
    #[cfg(not(unix))]
    let term = std::future::pending::<()>();
    tokio::select! { _ = ctrl_c => {}, _ = term => {} }
}

#[tokio::main]
async fn main() -> ExitCode {
    let predictor = match Predictor::new(
        &getenv("ORT_LIB", "/usr/local/lib/libonnxruntime.so"),
        &getenv("MODEL_PATH", "artifacts/model_sklearn.onnx"),
    ) {
        Ok(p) => p,
        Err(e) => {
            eprintln!("{e}");
            return ExitCode::FAILURE;
        }
    };
    let model_impl = getenv("MODEL_IMPL", "onnx-rust");
    let addr = format!("0.0.0.0:{}", getenv("PORT", "8080"));
    let listener = match tokio::net::TcpListener::bind(&addr).await {
        Ok(l) => l,
        Err(e) => {
            eprintln!("{addr} 에서 들을 수 없다: {e}");
            return ExitCode::FAILURE;
        }
    };
    eprintln!("delivery-eta (Rust) listening on {addr}, model={model_impl}");
    let state = Arc::new(AppState { predictor, model_impl });
    if let Err(e) = axum::serve(listener, app(state)).with_graceful_shutdown(shutdown_signal()).await {
        eprintln!("서버 오류: {e}");
        return ExitCode::FAILURE;
    }
    ExitCode::SUCCESS
}
