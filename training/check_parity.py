"""Python API 와 다른 언어 서비스(Go, Rust)의 예측이 같은지 비교한다.

기준은 학습한 원본 모델(MODEL_IMPL=sklearn)로 띄운 Python API 다.
서버가 떠 있어야 하므로 pytest 가 아니라 직접 실행한다.

    uv run python -m training.check_parity

환경 변수
  PY_URL   기준 Python API 주소 (기본 http://127.0.0.1:8080)
  TARGETS  비교 대상 "이름=주소" 목록, 쉼표 구분
           (기본 go=http://127.0.0.1:8081,rust=http://127.0.0.1:8082)

판정: 대상별 최대 오차가 TOLERANCE(1e-4분) 미만이면 PASS.
하나라도 FAIL 이면 exit 1 (CI 가 멈추도록).
"""
import json
import math
import os
import random
import sys
import urllib.error
import urllib.request

TOLERANCE = 1e-4  # 분
TIMEOUT = 5.0     # 초. 요청 하나당
EXPECTED_BASE_MODEL = "sklearn"

FIXED_CASES = [(3.0, 2), (0.0, 0), (8.0, 10)]
RANDOM_SEED = 0
RANDOM_COUNT = 20


def build_cases() -> list[tuple[float, int]]:
    """고정 입력 3건 + seed 0 으로 뽑은 무작위 20건 (거리 0.5~8km, 주문 0~10건)."""
    rng = random.Random(RANDOM_SEED)
    rand = [(rng.uniform(0.5, 8.0), rng.randint(0, 10)) for _ in range(RANDOM_COUNT)]
    return FIXED_CASES + rand


def parse_targets(raw: str) -> dict[str, str]:
    """"go=http://...,rust=http://..." 를 {이름: 주소} 로 바꾼다."""
    targets = {}
    for item in raw.split(","):
        item = item.strip()
        if not item:
            continue
        name, sep, url = item.partition("=")
        if not sep or not name.strip() or not url.strip():
            sys.exit(f"TARGETS 형식 오류: {item!r} (이름=주소 형식이어야 한다)")
        targets[name.strip()] = url.strip().rstrip("/")
    if not targets:
        sys.exit("TARGETS 가 비어 있다")
    return targets


def get_json(url: str) -> dict:
    with urllib.request.urlopen(url, timeout=TIMEOUT) as res:
        return json.load(res)


def predict(base_url: str, distance_km: float, pending_orders: int) -> float:
    """POST /eta 를 호출해 minutes 를 돌려준다."""
    body = json.dumps({"distance_km": distance_km, "pending_orders": pending_orders}).encode()
    req = urllib.request.Request(
        f"{base_url}/eta", data=body, method="POST",
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=TIMEOUT) as res:
        data = json.load(res)
    minutes = data["minutes"]
    if not isinstance(minutes, (int, float)) or isinstance(minutes, bool):
        raise ValueError(f"minutes 가 숫자가 아니다: {minutes!r}")
    return float(minutes)


def describe_error(e: Exception) -> str:
    if isinstance(e, urllib.error.HTTPError):
        return f"HTTP {e.code}"
    if isinstance(e, urllib.error.URLError):
        return f"연결 실패 ({e.reason})"
    return f"{type(e).__name__}: {e}"


def main() -> int:
    # Windows 에서 출력을 파이프로 넘기면 cp1252 가 되어 한글 출력에서 죽는다.
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    py_url = os.getenv("PY_URL", "http://127.0.0.1:8080").rstrip("/")
    targets = parse_targets(os.getenv("TARGETS", "go=http://127.0.0.1:8081,rust=http://127.0.0.1:8082"))
    cases = build_cases()

    # 기준 서버가 원본 모델로 떠 있는지 먼저 확인한다. 아니면 비교 자체가 무의미하다.
    try:
        base_model = get_json(f"{py_url}/health").get("model")
    except Exception as e:
        print(f"FAIL 기준 Python API({py_url}) 에 접속할 수 없다: {describe_error(e)}")
        return 1
    if base_model != EXPECTED_BASE_MODEL:
        print(f"FAIL 기준 Python API 의 모델이 {base_model!r} 이다 "
              f"(MODEL_IMPL={EXPECTED_BASE_MODEL} 로 띄워야 한다)")
        return 1

    try:
        expected = [predict(py_url, d, n) for d, n in cases]
    except Exception as e:
        print(f"FAIL 기준 Python API 예측 실패: {describe_error(e)}")
        return 1

    print(f"기준: {py_url} (model={base_model}), 입력 {len(cases)}건, 허용 오차 < {TOLERANCE}분")
    all_ok = True
    for name, url in targets.items():
        worst = (0.0, None)  # (오차, 입력)
        try:
            for (d, n), exp in zip(cases, expected):
                err = abs(predict(url, d, n) - exp)
                if not math.isfinite(err):  # NaN 은 비교가 항상 False 라 놓치기 쉽다
                    err = math.inf
                if worst[1] is None or err > worst[0]:
                    worst = (err, (d, n))
        except Exception as e:
            print(f"FAIL [{name}] {url}: {describe_error(e)}")
            all_ok = False
            continue
        max_err, (d, n) = worst
        ok = max_err < TOLERANCE
        all_ok &= ok
        print(f"{'PASS' if ok else 'FAIL'} [{name}] {url}: 최대 오차 {max_err:.2e}분 "
              f"(distance_km={d:.3f}, pending_orders={n})")

    return 0 if all_ok else 1


if __name__ == "__main__":
    sys.exit(main())
