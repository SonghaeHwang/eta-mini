"""테스트 공통 설정.

예상값은 rule 구현체 기준이다. api.app 은 import 시점에 MODEL_IMPL 을 읽으므로,
어떤 테스트 모듈이 먼저 import 되더라도 구현체가 rule 로 고정되도록
수집 단계(conftest 로드)에서 환경 변수를 설정한다.
"""

import os

os.environ["MODEL_IMPL"] = "rule"
