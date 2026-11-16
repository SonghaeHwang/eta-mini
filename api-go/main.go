// delivery-eta 의 Go 구현. api/app.py 와 같은 계약(POST /eta, GET /health)을 따른다.
//
// 예측은 ONNX 모델(입력 "X" [1, 2] float32 → 출력 "variable")을 ONNX Runtime 으로 실행한다.
// 정규화는 모델 안에 있으므로 원래 단위의 값을 그대로 넣는다.
//
// 파일 구성
//
//	main.go      설정, ONNX Runtime 예측기, 서버 시작
//	eta.go       HTTP 계층 (라우팅, /eta·/health 핸들러, 응답 형식)
//	validate.go  /eta 요청 검증 (Pydantic lax 규칙)
//
// 환경 변수
//
//	PORT              듣는 포트 (기본 8080)
//	MODEL_PATH        ONNX 모델 경로 (기본 artifacts/model_sklearn.onnx)
//	ONNXRUNTIME_LIB   libonnxruntime.so 경로 (기본 /usr/local/lib/libonnxruntime.so)
//	MODEL_IMPL        응답의 model 값 (기본 onnx-go)
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	ort "github.com/yalue/onnxruntime_go"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// onnxPredictor 는 ONNX 세션 하나를 감싼다. ORT 의 Run 은 동시 호출에 안전하고,
// 텐서는 요청마다 새로 만들므로 요청끼리 공유하는 상태가 없다.
type onnxPredictor struct {
	session *ort.DynamicAdvancedSession
}

func newONNXPredictor(libPath, modelPath string) (*onnxPredictor, error) {
	ort.SetSharedLibraryPath(libPath)
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("ONNX Runtime 초기화 실패 (%s): %w", libPath, err)
	}
	s, err := ort.NewDynamicAdvancedSession(modelPath, []string{"X"}, []string{"variable"}, nil)
	if err != nil {
		return nil, fmt.Errorf("모델 로드 실패 (%s): %w", modelPath, err)
	}
	return &onnxPredictor{session: s}, nil
}

func (p *onnxPredictor) Predict(distanceKm, pendingOrders float32) (float64, error) {
	in, err := ort.NewTensor(ort.NewShape(1, 2), []float32{distanceKm, pendingOrders})
	if err != nil {
		return 0, err
	}
	defer in.Destroy()
	out, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 1))
	if err != nil {
		return 0, err
	}
	defer out.Destroy()
	if err := p.session.Run([]ort.Value{in}, []ort.Value{out}); err != nil {
		return 0, err
	}
	return float64(out.GetData()[0]), nil
}

func main() {
	p, err := newONNXPredictor(
		getenv("ONNXRUNTIME_LIB", "/usr/local/lib/libonnxruntime.so"),
		getenv("MODEL_PATH", "artifacts/model_sklearn.onnx"),
	)
	if err != nil {
		log.Fatal(err)
	}
	modelImpl := getenv("MODEL_IMPL", "onnx-go")
	addr := ":" + getenv("PORT", "8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           newMux(p, modelImpl),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	log.Printf("delivery-eta (Go, ONNX Runtime %s) listening on %s, model=%s", ort.GetVersion(), addr, modelImpl)
	log.Fatal(srv.ListenAndServe())
}
