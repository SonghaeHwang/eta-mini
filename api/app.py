import importlib
import math
import os
from fastapi import FastAPI, Request
from fastapi.encoders import jsonable_encoder
from fastapi.exceptions import RequestValidationError
from fastapi.responses import HTMLResponse, JSONResponse
from pathlib import Path
from pydantic import BaseModel, Field

MODEL_IMPL = os.getenv("MODEL_IMPL", "rule")
INDEX_HTML = (Path(__file__).parent / "index.html").read_text(encoding="utf-8")
estimate_delivery_minutes = importlib.import_module(f"api.predictors.{MODEL_IMPL}").estimate_delivery_minutes

class EtaRequest(BaseModel):
    """배달 예상 시간 요청"""
    # 모델이 더 많은 정보를 원하면 여기에 필드를 추가
    distance_km: float = Field(ge=0, allow_inf_nan=False, description="가게에서 배달지까지 거리(km)")
    pending_orders: int = Field(ge=0, description="지금 밀려 있는 주문 수")

class EtaResponse(BaseModel):
    """배달 예상 시간 응답"""
    minutes: float
    model: str  # 어떤 구현체가 낸 예측인지 (장애 추적용)

app = FastAPI(title="delivery-eta")

def _json_safe(value):
    """JSON 으로 표현할 수 없는 NaN·inf 를 문자열로 바꾼다."""
    if isinstance(value, float) and not math.isfinite(value):
        return str(value)
    if isinstance(value, dict):
        return {k: _json_safe(v) for k, v in value.items()}
    if isinstance(value, list):
        return [_json_safe(v) for v in value]
    return value

@app.exception_handler(RequestValidationError)
async def validation_exception_handler(request: Request, exc: RequestValidationError) -> JSONResponse:
    """기본 422 응답과 같은 형식이되, 입력값에 NaN·inf 가 있어도 500 이 나지 않게 한다."""
    return JSONResponse(status_code=422, content={"detail": _json_safe(jsonable_encoder(exc.errors()))})

@app.get("/", response_class=HTMLResponse, include_in_schema=False)
def index() -> str:
    """API 소개 페이지(HTML)를 돌려준다."""
    return INDEX_HTML

@app.get("/health")
def health() -> dict[str, str]:
    """서비스 상태와 현재 구현체 이름을 돌려준다."""
    return {"status": "ok", "model": MODEL_IMPL}

@app.post("/eta", response_model=EtaResponse)
def eta(req: EtaRequest) -> EtaResponse:
    """배달 예상 시간을 계산"""
    minutes = estimate_delivery_minutes(req.distance_km, req.pending_orders)
    return EtaResponse(minutes=minutes, model=MODEL_IMPL)

