"""rule 구현체(estimate_delivery_minutes) 단위 테스트."""

import pytest

from api.predictors.rule import estimate_delivery_minutes


def test_rule_zero_inputs_is_base_time():
    """거리 0, 대기 주문 0 이면 기본 10분이다."""
    assert estimate_delivery_minutes(0.0, 0) == pytest.approx(10.0)


@pytest.mark.parametrize(
    ("distance_km", "pending_orders", "expected"),
    [
        (1.0, 0, 13.0),
        (0.0, 1, 12.0),
        (2.0, 3, 22.0),
        (10.5, 4, 49.5),
    ],
)
def test_rule_known_values(distance_km, pending_orders, expected):
    """알려진 입력에 대해 10 + 3*거리 + 2*대기주문 값을 돌려준다."""
    assert estimate_delivery_minutes(distance_km, pending_orders) == pytest.approx(expected)


def test_rule_returns_float():
    """정수 입력을 줘도 float 을 돌려준다."""
    assert isinstance(estimate_delivery_minutes(1, 1), float)


def test_rule_does_not_validate_negative_inputs():
    """rule 함수는 입력을 검증하지 않고 공식대로 계산한다 (검증은 API 계층 책임)."""
    assert estimate_delivery_minutes(-1.0, 0) == pytest.approx(7.0)
    assert estimate_delivery_minutes(0.0, -1) == pytest.approx(8.0)


def test_rule_large_values():
    """큰 입력도 공식대로 계산한다."""
    assert estimate_delivery_minutes(1e6, 10**6) == pytest.approx(10 + 3e6 + 2e6)
