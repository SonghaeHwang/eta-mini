"""delivery-eta API 계약 테스트 (rule 구현체 기준)."""

import os

import pytest

# 예상값은 rule 구현체 기준이므로, 앱을 import 하기 전에 구현체를 고정한다.
os.environ["MODEL_IMPL"] = "rule"

from fastapi.testclient import TestClient  # noqa: E402

from api.app import app  # noqa: E402

client = TestClient(app)


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
