"""delivery-eta API 계약 테스트 (rule 구현체 기준)."""

import math
import os
import subprocess
import sys
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

# MODEL_IMPL=rule 고정은 conftest.py 에서 한다 (import 순서와 무관하게 적용되도록).
from api.app import app

# 서버 예외를 그대로 올리지 않고 500 응답으로 받아, 상태 코드로 계약을 확인한다.
client = TestClient(app, raise_server_exceptions=False)

PROJECT_ROOT = Path(__file__).resolve().parents[2]


def post_raw(body: str, content_type: str | None = "application/json"):
    """json= 으로 만들 수 없는 바디(NaN, Infinity 등)를 그대로 보낸다."""
    headers = {"Content-Type": content_type} if content_type else {}
    return client.post("/eta", content=body, headers=headers)


# ---------- / (소개 페이지) ----------

def test_index_returns_html_page():
    """/ 는 200 과 UTF-8 HTML 문서를 돌려준다."""
    res = client.get("/")
    assert res.status_code == 200
    assert res.headers["content-type"] == "text/html; charset=utf-8"
    assert res.text.lstrip().lower().startswith("<!doctype html>")


@pytest.mark.parametrize("text", ["delivery-eta", "/health", "/eta", "/docs", "distance_km", "pending_orders"])
def test_index_introduces_api(text):
    """소개 페이지는 서비스 이름, 각 엔드포인트, 요청 필드를 안내한다."""
    assert text in client.get("/").text


def test_index_post_method_not_allowed():
    """/ 는 GET 전용이라 POST 는 405 를 돌려준다."""
    assert client.post("/").status_code == 405


def test_index_is_not_in_openapi_schema():
    """소개 페이지는 JSON API 가 아니므로 OpenAPI 스키마에 넣지 않는다."""
    assert "/" not in client.get("/openapi.json").json()["paths"]


# ---------- /health ----------

def test_health_returns_ok_and_model_name():
    """/health 는 200 과 status=ok, 현재 구현체 이름을 돌려준다."""
    res = client.get("/health")
    assert res.status_code == 200
    assert res.json() == {"status": "ok", "model": "rule"}


# ---------- /eta 정상 입력 ----------

def test_eta_response_shape():
    """/eta 응답은 minutes(숫자)와 model(문자열) 두 필드만 가진다."""
    res = client.post("/eta", json={"distance_km": 2.0, "pending_orders": 3})
    assert res.status_code == 200
    body = res.json()
    assert set(body) == {"minutes", "model"}
    assert isinstance(body["minutes"], (int, float))
    assert body["model"] == "rule"


@pytest.mark.parametrize(
    ("distance_km", "pending_orders", "expected"),
    [
        (2.0, 3, 22.0),   # 10 + 3*2 + 2*3
        (1.5, 0, 14.5),   # 소수 거리
        (5, 1, 27.0),     # 정수로 보낸 거리
    ],
)
def test_eta_known_values(distance_km, pending_orders, expected):
    """알려진 입력에 대해 10 + 3*거리 + 2*대기주문 값을 돌려준다."""
    res = client.post(
        "/eta", json={"distance_km": distance_km, "pending_orders": pending_orders}
    )
    assert res.status_code == 200
    assert res.json()["minutes"] == pytest.approx(expected)


# ---------- /eta 경계값 ----------

def test_eta_zero_inputs_returns_base_time():
    """거리 0, 대기 주문 0 이면 기본 10분을 돌려준다."""
    res = client.post("/eta", json={"distance_km": 0, "pending_orders": 0})
    assert res.status_code == 200
    assert res.json()["minutes"] == pytest.approx(10.0)


def test_eta_large_values():
    """큰 거리와 많은 대기 주문도 200 으로 처리하고 공식대로 계산한다."""
    res = client.post(
        "/eta", json={"distance_km": 1_000_000.0, "pending_orders": 1_000_000}
    )
    assert res.status_code == 200
    # 큰 값에서는 approx 의 상대 오차(1e-6)가 커지므로 절대 오차로 비교한다.
    assert res.json()["minutes"] == pytest.approx(10 + 3_000_000 + 2_000_000, abs=1e-6)


def test_eta_increases_with_distance_and_orders():
    """거리나 대기 주문이 늘면 예상 시간도 늘어난다."""
    def minutes(d, p):
        return client.post("/eta", json={"distance_km": d, "pending_orders": p}).json()["minutes"]

    assert minutes(3, 0) > minutes(1, 0)
    assert minutes(1, 5) > minutes(1, 0)


# ---------- /eta 잘못된 입력 → 422 ----------

@pytest.mark.parametrize(
    "payload",
    [
        {"distance_km": -1.0, "pending_orders": 0},         # 음수 거리
        {"distance_km": 1.0, "pending_orders": -1},         # 음수 대기 주문
        {"distance_km": "abc", "pending_orders": 0},        # 숫자가 아닌 거리
        {"distance_km": 1.0, "pending_orders": "many"},     # 숫자가 아닌 대기 주문
        {"distance_km": 1.0, "pending_orders": 1.5},        # 정수가 아닌 대기 주문
        {"distance_km": None, "pending_orders": 0},         # null
        {"pending_orders": 0},                              # 거리 누락
        {"distance_km": 1.0},                               # 대기 주문 누락
        {},                                                 # 빈 바디
    ],
)
def test_eta_invalid_input_returns_422(payload):
    """음수·숫자가 아닌 값·누락된 필드는 422 를 돌려준다."""
    res = client.post("/eta", json=payload)
    assert res.status_code == 422


def test_eta_non_json_body_returns_422():
    """JSON 이 아닌 바디는 422 를 돌려준다."""
    res = client.post(
        "/eta", content="not json", headers={"Content-Type": "application/json"}
    )
    assert res.status_code == 422


def test_eta_get_method_not_allowed():
    """/eta 는 POST 전용이라 GET 은 405 를 돌려준다."""
    res = client.get("/eta")
    assert res.status_code == 405


# ---------- /eta 유한하지 않은 숫자 → 422 ----------
# 계약: 잘못된 입력은 422, 응답 minutes 는 항상 유한한 숫자다.

@pytest.mark.parametrize(
    "body",
    [
        '{"distance_km": NaN, "pending_orders": 0}',        # NaN
        '{"distance_km": Infinity, "pending_orders": 0}',   # 무한대
        '{"distance_km": 1e400, "pending_orders": 0}',      # float 범위 초과 → inf
    ],
)
def test_eta_non_finite_distance_returns_422(body):
    """NaN·무한대 거리는 500 이나 minutes=null 이 아니라 422 를 돌려준다."""
    res = post_raw(body)
    assert res.status_code == 422


def test_eta_huge_orders_returns_finite_minutes():
    """아주 큰 대기 주문 수(10**30)도 유한한 숫자 minutes 를 돌려준다."""
    res = post_raw('{"distance_km": 1, "pending_orders": %d}' % 10**30)
    assert res.status_code == 200
    minutes = res.json()["minutes"]
    assert isinstance(minutes, (int, float)) and math.isfinite(minutes)
    assert minutes == pytest.approx(10 + 3 + 2 * 10**30)


# ---------- /eta 경계값 (추가) ----------

def test_eta_tiny_negative_distance_returns_422():
    """0 바로 아래 음수 거리(-1e-9)도 422 를 돌려준다."""
    res = client.post("/eta", json={"distance_km": -1e-9, "pending_orders": 0})
    assert res.status_code == 422


def test_eta_negative_zero_distance_is_base_time():
    """-0.0 거리는 0 과 같게 취급해 기본 10분을 돌려준다."""
    res = post_raw('{"distance_km": -0.0, "pending_orders": 0}')
    assert res.status_code == 200
    assert res.json()["minutes"] == pytest.approx(10.0)


# ---------- /eta 잘못된 입력 (추가) ----------

@pytest.mark.parametrize(
    "payload",
    [
        {"distance_km": "", "pending_orders": 0},          # 빈 문자열 거리
        {"distance_km": 1.0, "pending_orders": "2.5"},     # 정수가 아닌 숫자 문자열
        {"distance_km": [1], "pending_orders": 0},         # 배열 거리
        {"distance_km": {"v": 1}, "pending_orders": 0},    # 객체 거리
    ],
)
def test_eta_more_invalid_values_return_422(payload):
    """빈 문자열·정수가 아닌 숫자 문자열·배열·객체 값은 422 를 돌려준다."""
    res = client.post("/eta", json=payload)
    assert res.status_code == 422


@pytest.mark.parametrize("body", ["[1, 2]", "null", '"text"', "3"])
def test_eta_non_object_body_returns_422(body):
    """JSON 이지만 객체가 아닌 바디(배열·null·문자열·숫자)는 422 를 돌려준다."""
    assert post_raw(body).status_code == 422


@pytest.mark.parametrize("content_type", [None, "text/plain"])
def test_eta_non_json_content_type_returns_422(content_type):
    """Content-Type 이 없거나 JSON 이 아니면 422 를 돌려준다."""
    res = post_raw('{"distance_km": 1, "pending_orders": 0}', content_type)
    assert res.status_code == 422


@pytest.mark.parametrize(
    ("payload", "bad_field"),
    [
        ({"distance_km": -1.0, "pending_orders": 0}, "distance_km"),
        ({"distance_km": 1.0, "pending_orders": "many"}, "pending_orders"),
        ({"distance_km": 1.0}, "pending_orders"),
    ],
)
def test_eta_422_body_points_to_bad_field(payload, bad_field):
    """422 응답은 detail 목록을 가지고, loc 이 문제 필드를 가리킨다."""
    res = client.post("/eta", json=payload)
    assert res.status_code == 422
    detail = res.json()["detail"]
    assert isinstance(detail, list) and detail
    assert ["body", bad_field] in [d["loc"] for d in detail]


# ---------- /eta 느슨한 타입 변환 (현재 정책) ----------
# FastAPI/Pydantic 기본(lax) 동작을 현재 계약으로 고정한다.
# 정책을 엄격하게 바꾸면 이 섹션의 기대값을 422 로 바꿔야 한다.

@pytest.mark.parametrize(
    ("payload", "expected"),
    [
        ({"distance_km": "2", "pending_orders": 0}, 16.0),     # 숫자 문자열 거리
        ({"distance_km": 1, "pending_orders": "3"}, 19.0),     # 숫자 문자열 대기 주문
        ({"distance_km": 1, "pending_orders": 2.0}, 17.0),     # 소수부 0 인 float
        ({"distance_km": True, "pending_orders": 0}, 13.0),    # bool 거리 → 1
        ({"distance_km": 1, "pending_orders": True}, 15.0),    # bool 대기 주문 → 1
    ],
)
def test_eta_lax_coercion_current_policy(payload, expected):
    """현재 정책: 숫자 문자열·소수부 0 float·bool 은 숫자로 변환해 계산한다."""
    res = client.post("/eta", json=payload)
    assert res.status_code == 200
    assert res.json()["minutes"] == pytest.approx(expected)


def test_eta_extra_field_is_ignored_current_policy():
    """현재 정책: 정의되지 않은 필드는 무시하고 200 을 돌려준다."""
    res = client.post("/eta", json={"distance_km": 1, "pending_orders": 0, "x": 1})
    assert res.status_code == 200
    assert res.json() == {"minutes": pytest.approx(13.0), "model": "rule"}


# ---------- 라우팅 ----------

def test_health_post_method_not_allowed():
    """/health 는 GET 전용이라 POST 는 405 를 돌려준다."""
    assert client.post("/health").status_code == 405


def test_unknown_path_returns_404():
    """정의되지 않은 경로는 404 를 돌려준다."""
    assert client.get("/nope").status_code == 404


# ---------- 설정 ----------

def test_unknown_model_impl_fails_at_import():
    """없는 MODEL_IMPL 이면 앱 import 가 ModuleNotFoundError 로 실패한다."""
    env = {**os.environ, "MODEL_IMPL": "does_not_exist", "PYTHONPATH": str(PROJECT_ROOT)}
    proc = subprocess.run(
        [sys.executable, "-c", "import api.app"],
        cwd=PROJECT_ROOT, env=env, capture_output=True, text=True, timeout=60,
    )
    assert proc.returncode != 0
    assert "ModuleNotFoundError" in proc.stderr
