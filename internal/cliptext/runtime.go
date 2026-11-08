//go:build (windows || linux || darwin) && (amd64 || arm64)

package cliptext

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Only the stable ONNX Runtime C API v23 functions needed by this model are
// bound. Offsets are zero-based slots in OrtApi from onnxruntime_c_api.h v1.23.2.
// Native handles are owned by Encoder; calls and Close are serialized.
type Encoder struct {
	mu                            sync.Mutex
	tokenizer                     *Tokenizer
	library, env, session, memory uintptr
	createTensor                  func(uintptr, unsafe.Pointer, uintptr, *int64, uintptr, int32, *uintptr) uintptr
	run                           func(uintptr, uintptr, *uintptr, *uintptr, uintptr, *uintptr, uintptr, *uintptr) uintptr
	tensorData                    func(uintptr, *unsafe.Pointer) uintptr
	tensorShape                   func(uintptr, *uintptr) uintptr
	elementCount                  func(uintptr, *uintptr) uintptr
	releaseShape                  func(uintptr)
	releaseValue                  func(uintptr)
	releaseSession                func(uintptr)
	releaseEnv                    func(uintptr)
	releaseMemory                 func(uintptr)
	errorMessage                  func(uintptr) string
	releaseStatus                 func(uintptr)
}

func Open(directory string, threads int) (_ *Encoder, err error) {
	directory, err = filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	tokenizer, err := LoadTokenizer(filepath.Join(directory, "tokenizer.json"))
	if err != nil {
		return nil, err
	}
	e := &Encoder{tokenizer: tokenizer}
	e.library, err = openLibrary(filepath.Join(directory, LibraryName()))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			e.Close()
		}
	}()
	baseSymbol, err := lookupLibrary(e.library, "OrtGetApiBase")
	if err != nil {
		return nil, err
	}
	var getBase func() unsafe.Pointer
	purego.RegisterFunc(&getBase, baseSymbol)
	base := getBase()
	if base == nil {
		return nil, fmt.Errorf("ONNX Runtime has no API base")
	}
	var getAPI func(uint32) unsafe.Pointer
	purego.RegisterFunc(&getAPI, *(*uintptr)(base))
	table := getAPI(23)
	if table == nil {
		return nil, fmt.Errorf("ONNX Runtime C API 23 unavailable")
	}
	bind := func(fn any, index int) {
		p := unsafe.Slice((*uintptr)(table), 131)[index]
		purego.RegisterFunc(fn, p)
	}
	bind(&e.errorMessage, 2)
	bind(&e.releaseStatus, 93)
	bind(&e.releaseEnv, 92)
	bind(&e.releaseSession, 95)
	bind(&e.releaseMemory, 94)
	bind(&e.releaseValue, 96)
	bind(&e.releaseShape, 99)
	bind(&e.createTensor, 49)
	bind(&e.run, 9)
	bind(&e.tensorData, 51)
	bind(&e.tensorShape, 65)
	bind(&e.elementCount, 64)
	var createEnv func(int32, string, *uintptr) uintptr
	bind(&createEnv, 3)
	if err = e.check(createEnv(3, "home-images-text", &e.env)); err != nil {
		return nil, err
	}
	var createMemory func(int32, int32, *uintptr) uintptr
	bind(&createMemory, 69)
	if err = e.check(createMemory(1, 0, &e.memory)); err != nil {
		return nil, err
	}
	var createOptions func(*uintptr) uintptr
	bind(&createOptions, 10)
	var options uintptr
	if err = e.check(createOptions(&options)); err != nil {
		return nil, err
	}
	var releaseOptions func(uintptr)
	bind(&releaseOptions, 100)
	defer releaseOptions(options)
	var setThreads func(uintptr, int32) uintptr
	bind(&setThreads, 24)
	if err = e.check(setThreads(options, int32(max(1, min(threads, 4))))); err != nil {
		return nil, err
	}
	var setInter func(uintptr, int32) uintptr
	bind(&setInter, 25)
	if err = e.check(setInter(options, 1)); err != nil {
		return nil, err
	}
	var setOptimization func(uintptr, int32) uintptr
	bind(&setOptimization, 23)
	if err = e.check(setOptimization(options, 99)); err != nil {
		return nil, err
	}
	var setConfig func(uintptr, string, string) uintptr
	bind(&setConfig, 130)
	if err = e.check(setConfig(options, "session.intra_op.allow_spinning", "0")); err != nil {
		return nil, err
	}
	var createSession func(uintptr, unsafe.Pointer, uintptr, *uintptr) uintptr
	bind(&createSession, 7)
	path := nativePath(filepath.Join(directory, "model.onnx"))
	var pointer unsafe.Pointer
	switch p := path.(type) {
	case []byte:
		pointer = unsafe.Pointer(&p[0])
	case []uint16:
		pointer = unsafe.Pointer(&p[0])
	}
	err = e.check(createSession(e.env, pointer, options, &e.session))
	runtime.KeepAlive(path)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Encoder) check(status uintptr) error {
	if status == 0 {
		return nil
	}
	message := e.errorMessage(status)
	e.releaseStatus(status)
	return fmt.Errorf("ONNX Runtime: %s", message)
}

func (e *Encoder) Encode(ctx context.Context, query string) ([]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.session == 0 {
		return nil, fmt.Errorf("text encoder is closed")
	}
	ids, err := e.tokenizer.Encode(query)
	if err != nil {
		return nil, err
	}
	shape := [2]int64{1, ContextLength}
	var input uintptr
	if err = e.check(e.createTensor(e.memory, unsafe.Pointer(&ids[0]), ContextLength*4, &shape[0], 2, 6, &input)); err != nil {
		return nil, err
	}
	defer e.releaseValue(input)
	inputName, outputName := append([]byte("text"), 0), append([]byte("embedding"), 0)
	inputs, outputs := [1]uintptr{uintptr(unsafe.Pointer(&inputName[0]))}, [1]uintptr{uintptr(unsafe.Pointer(&outputName[0]))}
	var output uintptr
	err = e.check(e.run(e.session, 0, &inputs[0], &input, 1, &outputs[0], 1, &output))
	runtime.KeepAlive(ids)
	runtime.KeepAlive(shape)
	runtime.KeepAlive(inputName)
	runtime.KeepAlive(outputName)
	if output != 0 {
		defer e.releaseValue(output)
	}
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	var info uintptr
	if err = e.check(e.tensorShape(output, &info)); err != nil {
		return nil, err
	}
	defer e.releaseShape(info)
	var count uintptr
	if err = e.check(e.elementCount(info, &count)); err != nil {
		return nil, err
	}
	if count != Dimensions {
		return nil, fmt.Errorf("CLIP output dimension %d, expected %d", count, Dimensions)
	}
	var data unsafe.Pointer
	if err = e.check(e.tensorData(output, &data)); err != nil {
		return nil, err
	}
	values := append([]float32(nil), unsafe.Slice((*float32)(data), Dimensions)...)
	var norm float64
	for _, x := range values {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return nil, fmt.Errorf("nonfinite CLIP output")
		}
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return nil, fmt.Errorf("empty CLIP output")
	}
	scale := float32(math.Sqrt(norm))
	for i := range values {
		values[i] /= scale
	}
	return values, nil
}
func (e *Encoder) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session != 0 {
		e.releaseSession(e.session)
		e.session = 0
	}
	if e.memory != 0 {
		e.releaseMemory(e.memory)
		e.memory = 0
	}
	if e.env != 0 {
		e.releaseEnv(e.env)
		e.env = 0
	}
	if e.library != 0 {
		closeLibrary(e.library)
		e.library = 0
	}
}
